package com.l00prite.os;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.content.pm.ServiceInfo;
import android.net.ConnectivityManager;
import android.net.LinkProperties;
import android.net.Network;
import android.os.Build;
import android.os.IBinder;
import android.util.Log;

import java.io.BufferedReader;
import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.net.InetAddress;
import java.util.List;
import java.util.Map;

/**
 * Foreground service that owns the lifecycle of the l00prite gateway process (see
 * cli-os/docs/android-architecture.md section 3, "boot sequence").
 *
 * Responsibilities, in order, on every (re)start: prepare the app-private environment
 * (master key, setup secret, CA bundle, DNS list), exec the gateway native payload with
 * that environment, drain its stdout to logcat, and supervise it -- restarting on crash
 * with a capped, backed-off retry policy. Kept deliberately minimal: no binder API, no
 * "isRunning" query surface. Callers just start it; MainActivity discovers readiness by
 * polling the gateway's own /healthz over loopback.
 */
public class GatewayService extends Service {

    private static final String TAG = "l00prite-gw";
    private static final String CHANNEL_ID = "l00prite";
    private static final int NOTIFICATION_ID = 1;
    private static final String ASSET_CACERT = "cacert.pem";

    /** Restart policy: stop retrying after this many crashes in a row... */
    private static final int MAX_RAPID_RESTARTS = 5;
    /** ...waiting this long between each retry... */
    private static final long RESTART_BACKOFF_MS = 2000L;
    /** ...but forgive the crash count once a run has been stable this long. */
    private static final long STABLE_RUN_MS = 60000L;

    private volatile boolean stopping = false;
    private volatile Process gatewayProcess;
    private Thread monitorThread;

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        startForegroundNotification();
        // isAlive(), not just a null check: runAndSupervise() itself returns (leaving a dead,
        // non-null Thread object behind) when the launch fails outright or the rapid-crash limit is
        // hit -- a later onStartCommand (e.g. reopening the app) would otherwise see a non-null
        // monitorThread forever and never restart the gateway again until the whole service process
        // is killed (Codex review, PR #7).
        if (monitorThread == null || !monitorThread.isAlive()) {
            monitorThread = new Thread(new Runnable() {
                @Override
                public void run() {
                    runAndSupervise();
                }
            }, "l00prite-gw-monitor");
            monitorThread.setDaemon(true);
            monitorThread.start();
        }
        return START_STICKY;
    }

    @Override
    public void onDestroy() {
        stopping = true;
        Process p = gatewayProcess;
        if (p != null) {
            p.destroy();
        }
        super.onDestroy();
    }

    private void startForegroundNotification() {
        NotificationManager nm = (NotificationManager) getSystemService(Context.NOTIFICATION_SERVICE);
        NotificationChannel channel = new NotificationChannel(
                CHANNEL_ID, "L00prite OS", NotificationManager.IMPORTANCE_LOW);
        channel.setDescription("Keeps the on-device L00prite gateway running.");
        nm.createNotificationChannel(channel);

        Notification notification = new Notification.Builder(this, CHANNEL_ID)
                .setContentTitle("L00prite OS")
                .setContentText("Gateway running")
                .setSmallIcon(android.R.drawable.ic_menu_info_details)
                .setOngoing(true)
                .build();

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            startForeground(NOTIFICATION_ID, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC);
        } else {
            startForeground(NOTIFICATION_ID, notification);
        }
    }

    private void runAndSupervise() {
        int rapidRestarts = 0;
        while (!stopping) {
            long startedAt = System.currentTimeMillis();
            try {
                gatewayProcess = launchGateway();
            } catch (IOException e) {
                Log.e(TAG, "failed to launch gateway process; giving up", e);
                return;
            }

            drainOutput(gatewayProcess);

            int exitCode;
            try {
                exitCode = gatewayProcess.waitFor();
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                return;
            }

            if (stopping) {
                return;
            }

            long ranMs = System.currentTimeMillis() - startedAt;
            Log.w(TAG, "gateway process exited (code " + exitCode + ") after " + ranMs + "ms");

            if (ranMs >= STABLE_RUN_MS) {
                rapidRestarts = 0;
            }
            rapidRestarts++;
            if (rapidRestarts > MAX_RAPID_RESTARTS) {
                Log.e(TAG, "gateway process crashed " + rapidRestarts
                        + " times in a row; not restarting again");
                return;
            }

            try {
                Thread.sleep(RESTART_BACKOFF_MS);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                return;
            }
        }
    }

    private Process launchGateway() throws IOException {
        File home = new File(getFilesDir(), "l00prite");
        home.mkdirs();

        File cacert = extractCacertIfNeeded();
        String dns = resolveDnsServers();

        File exe = new File(getApplicationInfo().nativeLibraryDir, "libl00prite.so");

        ProcessBuilder pb = new ProcessBuilder(exe.getAbsolutePath(), "serve");
        pb.redirectErrorStream(true);
        pb.directory(getFilesDir());

        Map<String, String> env = pb.environment();
        env.put("LOOPRITE_HOME", home.getAbsolutePath());
        env.put("HOME", getFilesDir().getAbsolutePath());
        env.put("LOOPRITE_PORT", "8787");
        env.put("LOOPRITE_MASTER_KEY", Keys.getOrCreateMasterKeyBase64(this));
        env.put("LOOPRITE_SETUP_SECRET", Keys.getOrCreateSetupSecret(this));
        env.put("SSL_CERT_FILE", cacert.getAbsolutePath());
        env.put("LOOPRITE_DNS", dns);
        env.put("GIT_AUTHOR_NAME", "l00prite-os");
        env.put("GIT_AUTHOR_EMAIL", "l00prite-os@localhost");
        env.put("GIT_COMMITTER_NAME", "l00prite-os");
        env.put("GIT_COMMITTER_EMAIL", "l00prite-os@localhost");

        Log.i(TAG, "starting gateway: " + exe.getAbsolutePath() + " serve (LOOPRITE_DNS=" + dns + ")");
        return pb.start();
    }

    /**
     * Extracts assets/cacert.pem into filesDir, overwriting any previous copy.
     *
     * Always re-extracts rather than trying to skip up-to-date copies: InputStream#available()
     * is documented as an ESTIMATE of remaining bytes, not a reliable total-length signal (and is
     * not guaranteed to equal the asset's real size for a compressed APK asset entry), so
     * comparing it against the on-disk file size could either re-extract every boot for no
     * reason or, worse, decide a stale copy is "already up to date" after an app update changes
     * the bundled CA bundle. The file is ~130KB; unconditional extraction on every service start
     * is not worth the risk to get an optimization only.
     */
    private File extractCacertIfNeeded() throws IOException {
        File out = new File(getFilesDir(), ASSET_CACERT);
        InputStream in = getAssets().open(ASSET_CACERT);
        try {
            FileOutputStream fos = new FileOutputStream(out);
            try {
                byte[] buf = new byte[8192];
                int n;
                while ((n = in.read(buf)) >= 0) {
                    fos.write(buf, 0, n);
                }
            } finally {
                fos.close();
            }
        } finally {
            in.close();
        }
        return out;
    }

    /**
     * Resolves the device's current DNS servers via ConnectivityManager so the gateway's
     * Go resolver (which cannot read /etc/resolv.conf on Android) can reach providers.
     * Falls back to public resolvers on any failure (gap G1).
     */
    private String resolveDnsServers() {
        String fallback = "8.8.8.8,1.1.1.1";
        try {
            ConnectivityManager cm = (ConnectivityManager) getSystemService(Context.CONNECTIVITY_SERVICE);
            if (cm == null) {
                return fallback;
            }
            Network network = cm.getActiveNetwork();
            if (network == null) {
                return fallback;
            }
            LinkProperties lp = cm.getLinkProperties(network);
            if (lp == null) {
                return fallback;
            }
            List<InetAddress> servers = lp.getDnsServers();
            if (servers == null || servers.isEmpty()) {
                return fallback;
            }
            StringBuilder sb = new StringBuilder();
            for (int i = 0; i < servers.size(); i++) {
                if (i > 0) {
                    sb.append(",");
                }
                sb.append(servers.get(i).getHostAddress());
            }
            return sb.toString();
        } catch (Exception e) {
            Log.w(TAG, "DNS resolution via ConnectivityManager failed; using fallback", e);
            return fallback;
        }
    }

    private void drainOutput(final Process process) {
        Thread t = new Thread(new Runnable() {
            @Override
            public void run() {
                BufferedReader reader = new BufferedReader(new InputStreamReader(process.getInputStream()));
                try {
                    String line;
                    while ((line = reader.readLine()) != null) {
                        Log.i(TAG, line);
                    }
                } catch (IOException e) {
                    // Pipe closed because the process exited; nothing more to drain.
                } finally {
                    try {
                        reader.close();
                    } catch (IOException ignored) {
                        // best-effort cleanup
                    }
                }
            }
        }, "l00prite-gw-stdout");
        t.setDaemon(true);
        t.start();
    }
}
