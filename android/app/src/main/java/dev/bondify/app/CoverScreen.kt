package dev.bondify.app

/**
 * One UI's cover-screen continuity (the Z Flip line's front display) resizes whatever
 * activity is in the foreground down to a few hundred dp of width when the phone is closed,
 * rather than launching a distinct app. There is no public API to detect "this is a cover
 * display" directly, so this is a width-based heuristic instead.
 *
 * The threshold is deliberately conservative (normal single-pane phone layouts are ~360dp+
 * wide in portrait) but has not been checked against a real Z Flip 5's actual cover-screen
 * width in dp -- no physical device was available to measure it. If it turns out to be wrong
 * for that device, adjust the constant; the rest of the compact-layout plumbing doesn't
 * change.
 */
object CoverScreen {
    const val COMPACT_WIDTH_THRESHOLD_DP = 340

    fun isCompactWidth(widthDp: Int): Boolean = widthDp in 1..COMPACT_WIDTH_THRESHOLD_DP
}
