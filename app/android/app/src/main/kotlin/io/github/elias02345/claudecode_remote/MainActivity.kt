package io.github.elias02345.claudecode_remote

import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Bundle
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.MethodChannel
import java.io.File

/**
 * Receives files shared into the app and hands their local paths to Dart.
 *
 * The manifest advertises ACTION_SEND and ACTION_SEND_MULTIPLE, so the app
 * appears in every share sheet on the device. Without this class it appeared
 * there and then did nothing at all — the intent arrived, no code read it, and
 * the user watched their file go nowhere.
 *
 * A shared item is a content:// URI, not a path. The sending app grants read
 * permission for it to this task only, and that grant does not outlive the
 * activity — so the content is copied into our own cache directory here and
 * Dart is given a real file it can still open later. Handing Dart the URI
 * instead would work right up until the upload retried after a reconnect.
 */
class MainActivity : FlutterActivity() {
    private var channel: MethodChannel? = null

    /** Shares that arrived before Dart was listening. */
    private val pending = mutableListOf<String>()

    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)

        val ch = MethodChannel(flutterEngine.dartExecutor.binaryMessenger, CHANNEL)
        channel = ch
        ch.setMethodCallHandler { call, result ->
            when (call.method) {
                // Dart asks once at startup. A share that launched the app cold
                // arrives before the engine exists, so it cannot be pushed —
                // it has to be available to pull.
                "takePendingShares" -> {
                    val out = pending.toList()
                    pending.clear()
                    result.success(out)
                }
                else -> result.notImplemented()
            }
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        handleShare(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        // launchMode is singleTop, so a share into the running app lands here
        // rather than stacking a second activity on top of a live terminal.
        setIntent(intent)
        handleShare(intent)
    }

    private fun handleShare(intent: Intent?) {
        if (intent == null) return
        val uris: List<Uri> = when (intent.action) {
            Intent.ACTION_SEND -> listOfNotNull(intent.parcelableExtra(Intent.EXTRA_STREAM))
            Intent.ACTION_SEND_MULTIPLE -> intent.parcelableArrayListExtra(Intent.EXTRA_STREAM)
            else -> null
        } ?: return

        val paths = uris.mapNotNull { copyToCache(it) }
        if (paths.isEmpty()) return

        val ch = channel
        if (ch == null) {
            pending.addAll(paths)
        } else {
            ch.invokeMethod("sharedFiles", paths)
        }
    }

    /**
     * Copies the shared content into cacheDir and returns the absolute path,
     * or null if it could not be read.
     *
     * The filename comes from the sending app, so it is untrusted: path
     * separators and control characters are stripped before it becomes a real
     * file on disk. Dart sanitises the name again for display (see
     * `sanitiseFilename` in share_upload_service.dart) — this pass is about not
     * writing outside cacheDir in the first place.
     */
    private fun copyToCache(uri: Uri): String? {
        val name = displayName(uri) ?: "shared-file"
        val safe = name
            .replace(Regex("[\\x00-\\x1f\\x7f]"), "")
            .substringAfterLast('/')
            .substringAfterLast('\\')
            .ifEmpty { "shared-file" }

        val dir = File(cacheDir, "shared").apply { mkdirs() }
        val target = File(dir, safe)
        return try {
            contentResolver.openInputStream(uri)?.use { input ->
                target.outputStream().use { output -> input.copyTo(output) }
            } ?: return null
            target.absolutePath
        } catch (_: Exception) {
            // A share that cannot be read is not a crash — the sending app may
            // have revoked the grant already. Drop it and keep the others.
            null
        }
    }

    private fun displayName(uri: Uri): String? {
        contentResolver.query(uri, null, null, null, null)?.use { cursor ->
            val index = cursor.getColumnIndex(android.provider.OpenableColumns.DISPLAY_NAME)
            if (index >= 0 && cursor.moveToFirst()) {
                return cursor.getString(index)
            }
        }
        return uri.lastPathSegment
    }

    // getParcelableExtra(String) is deprecated from API 33; the typed overload
    // does not exist below it. One helper instead of a suppression at each call.
    private inline fun <reified T : android.os.Parcelable> Intent.parcelableExtra(key: String): T? =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            getParcelableExtra(key, T::class.java)
        } else {
            @Suppress("DEPRECATION")
            getParcelableExtra(key) as? T
        }

    private inline fun <reified T : android.os.Parcelable> Intent.parcelableArrayListExtra(
        key: String,
    ): ArrayList<T>? =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            getParcelableArrayListExtra(key, T::class.java)
        } else {
            @Suppress("DEPRECATION")
            getParcelableArrayListExtra(key)
        }

    companion object {
        const val CHANNEL = "io.github.elias02345.claudecode_remote/share"
    }
}
