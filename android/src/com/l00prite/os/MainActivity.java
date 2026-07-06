package com.l00prite.os;

import android.app.Activity;
import android.app.AlertDialog;
import android.content.ClipData;
import android.content.ClipboardManager;
import android.content.DialogInterface;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.database.Cursor;
import android.net.Uri;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.provider.DocumentsContract;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.Button;
import android.widget.FrameLayout;
import android.widget.TextView;

import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.security.SecureRandom;

/**
 * The app's single activity: starts the gateway service, then swaps a "starting"
 * message for a WebView pointed at the gateway's own UI once it answers /healthz (see
 * cli-os/docs/android-architecture.md section 3, "boot sequence").
 *
 * There is deliberately no client-side routing state: the server itself decides
 * wizard-vs-dashboard based on the setup latch, so a cold app start always lands
 * correctly regardless of what happened in a previous session.
 */
public class MainActivity extends Activity {

    private static final String HEALTHZ_URL = "http://127.0.0.1:8787/healthz";
    private static final String BASE_URL = "http://127.0.0.1:8787/";
    private static final long POLL_INTERVAL_MS = 500L;
    private static final long POLL_TIMEOUT_MS = 30000L;
    private static final String PERMISSION_POST_NOTIFICATIONS = "android.permission.POST_NOTIFICATIONS";
    private static final int NOTIFICATIONS_REQUEST_CODE = 1001;
    private static final int IMPORT_REPO_REQUEST_CODE = 2001;
    private static final String IMPORTED_REPOS_DIR = "imported-repos";

    private WebView webView;
    private TextView statusView;
    private Button importButton;
    private Thread pollerThread;
    private Thread importThread;
    private final Handler mainHandler = new Handler(Looper.getMainLooper());

    // Set on onDestroy(); checked by any Runnable the import thread posts to mainHandler so a
    // copy that finishes after the activity is gone never touches a dead UI (same discipline as
    // pollerThread.interrupt() below, belt-and-suspenders since interrupting a blocked stream
    // read is not guaranteed to be prompt).
    private volatile boolean activityDestroyed = false;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);

        startForegroundService(new Intent(this, GatewayService.class));
        requestNotificationPermissionIfNeeded();

        FrameLayout root = new FrameLayout(this);
        FrameLayout.LayoutParams fill = new FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT);

        statusView = new TextView(this);
        statusView.setText("Starting L00prite OS…");
        statusView.setGravity(Gravity.CENTER);
        statusView.setTextSize(18f);
        root.addView(statusView, fill);

        webView = new WebView(this);
        webView.getSettings().setJavaScriptEnabled(true);
        webView.getSettings().setDomStorageEnabled(true);
        webView.setWebViewClient(new WebViewClient());
        webView.setVisibility(View.GONE);
        root.addView(webView, fill);

        // Native "Import repo..." affordance, independent of the WebView/dashboard: closes the
        // gap where a repo reachable only via Android's Storage Access Framework (a synced
        // folder, a file manager, another app's exposed documents) has no way onto a real
        // on-device path the gateway's git tooling can operate on. See android-architecture.md
        // section 8 Phase 2. Deliberately a small, unobtrusive corner button, not part of the
        // dashboard UI it sits on top of.
        importButton = new Button(this);
        importButton.setText("Import repo...");
        FrameLayout.LayoutParams importParams = new FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        importParams.gravity = Gravity.BOTTOM | Gravity.END;
        int marginPx = (int) (16 * getResources().getDisplayMetrics().density);
        importParams.setMargins(marginPx, marginPx, marginPx, marginPx);
        importButton.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View v) {
                launchImportPicker();
            }
        });
        root.addView(importButton, importParams);

        setContentView(root);

        startHealthPoll();
    }

    /** Launches the system SAF folder picker. Framework Activity Result API (classic
     *  startActivityForResult/onActivityResult) — this app has no androidx.activity dependency. */
    private void launchImportPicker() {
        Intent intent = new Intent(Intent.ACTION_OPEN_DOCUMENT_TREE);
        try {
            startActivityForResult(intent, IMPORT_REPO_REQUEST_CODE);
        } catch (Exception e) {
            // No document-tree picker available on this device/ROM (e.g. no DocumentsUI).
            showImportError("No folder picker is available on this device.");
        }
    }

    @Override
    protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        super.onActivityResult(requestCode, resultCode, data);
        if (requestCode != IMPORT_REPO_REQUEST_CODE) {
            return;
        }
        if (resultCode != RESULT_OK || data == null || data.getData() == null) {
            // Cancelled or denied: leave the UI exactly as it was, nothing destructive.
            return;
        }
        final Uri treeUri = data.getData();

        // Take a persistable grant limited to read: this is an import/copy, we never write back
        // into the source tree. Only request the flag(s) the returned Intent actually carries —
        // takePersistableUriPermission throws if asked for a flag that wasn't granted.
        int grantedFlags = data.getFlags()
                & (Intent.FLAG_GRANT_READ_URI_PERMISSION | Intent.FLAG_GRANT_WRITE_URI_PERMISSION);
        int readFlag = grantedFlags & Intent.FLAG_GRANT_READ_URI_PERMISSION;
        if (readFlag == 0) {
            readFlag = Intent.FLAG_GRANT_READ_URI_PERMISSION;
        }
        try {
            getContentResolver().takePersistableUriPermission(treeUri, readFlag);
        } catch (Exception e) {
            // Not fatal for this one-off import: the grant from this Intent is still valid for
            // the lifetime of this process even if persisting it across reboots fails.
        }

        startImport(treeUri);
    }

    /** On API 33+, notifications require a runtime grant; the foreground-service
     *  notification is cosmetic (the service keeps running either way), so the result
     *  is intentionally ignored. */
    private void requestNotificationPermissionIfNeeded() {
        if (android.os.Build.VERSION.SDK_INT < 33) {
            return;
        }
        try {
            if (checkSelfPermission(PERMISSION_POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
                requestPermissions(new String[] { PERMISSION_POST_NOTIFICATIONS }, NOTIFICATIONS_REQUEST_CODE);
            }
        } catch (Exception e) {
            // Best-effort only.
        }
    }

    private void startHealthPoll() {
        final String setupSecret = Keys.getOrCreateSetupSecret(this);
        pollerThread = new Thread(new Runnable() {
            @Override
            public void run() {
                long deadline = System.currentTimeMillis() + POLL_TIMEOUT_MS;
                boolean up = false;
                while (System.currentTimeMillis() < deadline) {
                    if (Thread.currentThread().isInterrupted()) {
                        return;
                    }
                    if (isHealthy()) {
                        up = true;
                        break;
                    }
                    try {
                        Thread.sleep(POLL_INTERVAL_MS);
                    } catch (InterruptedException e) {
                        Thread.currentThread().interrupt();
                        return;
                    }
                }
                onPollFinished(up, setupSecret);
            }
        }, "l00prite-healthpoll");
        pollerThread.setDaemon(true);
        pollerThread.start();
    }

    @Override
    protected void onDestroy() {
        // The poller is a daemon thread (it will not itself prevent process exit), but an
        // un-interrupted instance can still outlive a destroyed activity across a configuration
        // change (e.g. rotation) and post a stale onPollFinished callback referencing this
        // instance — interrupting it here closes that leak/race.
        if (pollerThread != null) {
            pollerThread.interrupt();
        }
        // Same discipline for an in-flight repo import: flip the guard first so any Runnable
        // already queued on mainHandler no-ops, then best-effort interrupt the copy thread.
        activityDestroyed = true;
        if (importThread != null) {
            importThread.interrupt();
        }
        super.onDestroy();
    }

    private void onPollFinished(final boolean succeeded, final String setupSecret) {
        mainHandler.post(new Runnable() {
            @Override
            public void run() {
                if (succeeded) {
                    webView.loadUrl(BASE_URL + "?ss=" + setupSecret);
                    webView.setVisibility(View.VISIBLE);
                    statusView.setVisibility(View.GONE);
                } else {
                    statusView.setText("L00prite OS did not start in time. Please reopen the app.");
                }
            }
        });
    }

    private boolean isHealthy() {
        HttpURLConnection conn = null;
        try {
            URL url = new URL(HEALTHZ_URL);
            conn = (HttpURLConnection) url.openConnection();
            conn.setConnectTimeout(400);
            conn.setReadTimeout(400);
            conn.setRequestMethod("GET");
            int code = conn.getResponseCode();
            return code >= 200 && code < 300;
        } catch (Exception e) {
            return false;
        } finally {
            if (conn != null) {
                conn.disconnect();
            }
        }
    }

    @Override
    public void onBackPressed() {
        if (webView != null && webView.canGoBack()) {
            webView.goBack();
        } else {
            super.onBackPressed();
        }
    }

    // ---------------------------------------------------------------------------------------
    // SAF repo import: "Import repo..." button -> ACTION_OPEN_DOCUMENT_TREE ->
    // <filesDir>/imported-repos/<name> -> AlertDialog with the resulting path, for pasting into
    // the dashboard's existing "register an existing path" field.
    //
    // No android.provider.DocumentFile / androidx.documentfile.provider.DocumentFile is used
    // here: DocumentFile has only ever shipped as part of the (androidx) support library, never
    // as a platform class — it is not present in android.jar / android-all on any API level, and
    // this app takes no androidx dependency. The tree is walked directly with the framework's
    // android.provider.DocumentsContract (present since API 19) instead: getTreeDocumentId,
    // buildDocumentUriUsingTree, and buildChildDocumentsUriUsingTree/queries against
    // DocumentsContract.Document.COLUMN_* to enumerate and recurse.
    // ---------------------------------------------------------------------------------------

    private void startImport(final Uri treeUri) {
        setImportBusy(true);
        Thread t = new Thread(new Runnable() {
            @Override
            public void run() {
                runImport(treeUri);
            }
        }, "l00prite-import-repo");
        t.setDaemon(true);
        importThread = t;
        t.start();
    }

    private void setImportBusy(boolean busy) {
        importButton.setEnabled(!busy);
        importButton.setText(busy ? "Importing…" : "Import repo...");
    }

    /** Runs on the background "l00prite-import-repo" thread — never touches views directly. */
    private void runImport(Uri treeUri) {
        try {
            String rootDocId = DocumentsContract.getTreeDocumentId(treeUri);
            Uri rootDocUri = DocumentsContract.buildDocumentUriUsingTree(treeUri, rootDocId);
            String rawName = queryDisplayName(rootDocUri);
            String destName = sanitizeSegment(rawName);
            if (destName == null) {
                destName = "repo-" + randomSuffix();
            }

            File importsRoot = new File(getFilesDir(), IMPORTED_REPOS_DIR);
            if (!importsRoot.exists() && !importsRoot.mkdirs() && !importsRoot.isDirectory()) {
                throw new IOException("could not create " + IMPORTED_REPOS_DIR + "/");
            }
            File destDir = resolveDestDir(importsRoot, destName);
            if (!destDir.mkdirs()) {
                throw new IOException("could not create destination directory: " + destDir);
            }

            long[] fileCount = new long[1];
            copyTree(treeUri, rootDocId, destDir, importsRoot, fileCount);
            if (fileCount[0] == 0) {
                throw new IOException("Nothing readable was found in the selected folder.");
            }

            final String resultPath = destDir.getCanonicalPath();
            mainHandler.post(new Runnable() {
                @Override
                public void run() {
                    if (activityDestroyed) {
                        return;
                    }
                    setImportBusy(false);
                    showImportResultDialog(resultPath);
                }
            });
        } catch (final Exception e) {
            final String message = e.getMessage() != null ? e.getMessage() : e.getClass().getSimpleName();
            mainHandler.post(new Runnable() {
                @Override
                public void run() {
                    if (activityDestroyed) {
                        return;
                    }
                    setImportBusy(false);
                    showImportError(message);
                }
            });
        }
    }

    /**
     * Picks a not-yet-existing directory name under importsRoot for baseName, appending a random
     * suffix on collision (never a wall-clock timestamp — see the class-level note on why), then
     * verifies — via the fully resolved canonical path, not just the individual name segment —
     * that the result genuinely stays inside importsRoot before returning it. Defense in depth on
     * top of sanitizeSegment(), in case a future caller feeds this a less-trusted name.
     */
    private File resolveDestDir(File importsRoot, String baseName) throws IOException {
        File dest = new File(importsRoot, baseName);
        if (dest.exists()) {
            dest = new File(importsRoot, baseName + "-" + randomSuffix());
        }
        assertWithinRoot(dest, importsRoot);
        return dest;
    }

    /** Recursively copies every document under parentDocId into destDir, one child at a time. */
    private void copyTree(Uri treeUri, String parentDocId, File destDir, File importsRoot, long[] fileCount)
            throws IOException {
        Uri childrenUri = DocumentsContract.buildChildDocumentsUriUsingTree(treeUri, parentDocId);
        Cursor cursor = getContentResolver().query(childrenUri, new String[] {
                DocumentsContract.Document.COLUMN_DOCUMENT_ID,
                DocumentsContract.Document.COLUMN_DISPLAY_NAME,
                DocumentsContract.Document.COLUMN_MIME_TYPE
        }, null, null, null);
        if (cursor == null) {
            throw new IOException("could not list folder contents (permission or provider error)");
        }
        try {
            while (cursor.moveToNext()) {
                if (Thread.currentThread().isInterrupted()) {
                    throw new IOException("import cancelled");
                }
                String childDocId = cursor.getString(0);
                String rawName = cursor.getString(1);
                String mimeType = cursor.getString(2);

                String childName = sanitizeSegment(rawName);
                if (childName == null) {
                    childName = "_" + randomSuffix();
                }
                File childDest = new File(destDir, childName);
                assertWithinRoot(childDest, importsRoot);

                if (DocumentsContract.Document.MIME_TYPE_DIR.equals(mimeType)) {
                    // Deliberately not special-cased for ".git" or any other dot-prefixed name:
                    // faithfully importing a git checkout is the entire point of this feature.
                    if (!childDest.exists() && !childDest.mkdirs()) {
                        throw new IOException("could not create directory: " + childDest);
                    }
                    copyTree(treeUri, childDocId, childDest, importsRoot, fileCount);
                } else {
                    Uri childUri = DocumentsContract.buildDocumentUriUsingTree(treeUri, childDocId);
                    copyDocumentToFile(childUri, childDest);
                    fileCount[0]++;
                }
            }
        } finally {
            cursor.close();
        }
    }

    /** Byte-copy loop mirroring GatewayService.extractCacertIfNeeded's style. */
    private void copyDocumentToFile(Uri srcUri, File destFile) throws IOException {
        InputStream in = getContentResolver().openInputStream(srcUri);
        if (in == null) {
            throw new IOException("could not open document: " + srcUri);
        }
        try {
            FileOutputStream fos = new FileOutputStream(destFile);
            try {
                byte[] buf = new byte[8192];
                int n;
                while ((n = in.read(buf)) >= 0) {
                    if (Thread.currentThread().isInterrupted()) {
                        throw new IOException("import cancelled");
                    }
                    fos.write(buf, 0, n);
                }
            } finally {
                fos.close();
            }
        } finally {
            in.close();
        }
    }

    private String queryDisplayName(Uri documentUri) {
        Cursor cursor = null;
        try {
            cursor = getContentResolver().query(documentUri,
                    new String[] { DocumentsContract.Document.COLUMN_DISPLAY_NAME }, null, null, null);
            if (cursor != null && cursor.moveToFirst()) {
                return cursor.getString(0);
            }
        } catch (Exception e) {
            // Fall through to the caller's random-name fallback.
        } finally {
            if (cursor != null) {
                cursor.close();
            }
        }
        return null;
    }

    /**
     * Strips anything unsafe for a single filesystem path segment: '/' and '\\' (no nested
     * paths smuggled in via a crafted SAF display name), ASCII control characters, and the
     * literal segments "." / ".." (traversal). Deliberately does NOT strip a leading "." for
     * any other name — hidden/dot-prefixed entries such as ".git" or ".gitignore" must survive
     * unchanged for the import to be a faithful copy of a git checkout. Returns null (caller
     * substitutes a random fallback name) when nothing safe is left.
     */
    private static String sanitizeSegment(String rawName) {
        if (rawName == null) {
            return null;
        }
        String cleaned = rawName.replace('/', '_').replace('\\', '_');
        cleaned = cleaned.replaceAll("[\\x00-\\x1f]", "_").trim();
        if (cleaned.isEmpty() || cleaned.equals(".") || cleaned.equals("..")) {
            return null;
        }
        if (cleaned.length() > 200) {
            cleaned = cleaned.substring(0, 200);
        }
        return cleaned;
    }

    /**
     * Defense in depth beyond sanitizeSegment(): verifies candidate's fully RESOLVED canonical
     * path is still contained within root's canonical path before any write happens under it,
     * refusing otherwise. A per-segment string check alone would not catch every case a
     * filesystem might resolve unexpectedly (e.g. symlink traversal within the destination
     * tree), so this re-derives and checks the real path each call.
     */
    private static void assertWithinRoot(File candidate, File root) throws IOException {
        String rootCanonical = root.getCanonicalPath();
        String candidateCanonical = candidate.getCanonicalPath();
        if (!candidateCanonical.equals(rootCanonical)
                && !candidateCanonical.startsWith(rootCanonical + File.separator)) {
            throw new IOException("resolved path escaped " + IMPORTED_REPOS_DIR + "/: " + candidateCanonical);
        }
    }

    private static String randomSuffix() {
        byte[] buf = new byte[4];
        new SecureRandom().nextBytes(buf);
        StringBuilder sb = new StringBuilder(buf.length * 2);
        for (byte b : buf) {
            sb.append(String.format("%02x", b));
        }
        return sb.toString();
    }

    private void showImportResultDialog(final String path) {
        if (isFinishing() || isDestroyed()) {
            return;
        }
        new AlertDialog.Builder(this)
                .setTitle("Repo imported")
                .setMessage("Imported to:\n\n" + path
                        + "\n\nPaste this path into the dashboard's \"Register an existing path\" "
                        + "field to finish setup.")
                .setPositiveButton("Copy path", new DialogInterface.OnClickListener() {
                    @Override
                    public void onClick(DialogInterface dialog, int which) {
                        ClipboardManager clipboard =
                                (ClipboardManager) getSystemService(CLIPBOARD_SERVICE);
                        if (clipboard != null) {
                            clipboard.setPrimaryClip(ClipData.newPlainText("imported repo path", path));
                        }
                    }
                })
                .setNegativeButton("Close", null)
                .show();
    }

    private void showImportError(String message) {
        if (isFinishing() || isDestroyed()) {
            return;
        }
        new AlertDialog.Builder(this)
                .setTitle("Import failed")
                .setMessage(message)
                .setPositiveButton("OK", null)
                .show();
    }
}
