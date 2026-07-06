package com.l00prite.os;

import android.app.Activity;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.FrameLayout;
import android.widget.TextView;

import java.net.HttpURLConnection;
import java.net.URL;

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

    private WebView webView;
    private TextView statusView;
    private Thread pollerThread;
    private final Handler mainHandler = new Handler(Looper.getMainLooper());

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

        setContentView(root);

        startHealthPoll();
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
}
