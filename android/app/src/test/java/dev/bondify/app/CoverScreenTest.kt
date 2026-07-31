package dev.bondify.app

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class CoverScreenTest {
    @Test
    fun narrowCoverScreenWidthIsCompact() {
        assertTrue(CoverScreen.isCompactWidth(306))
        assertTrue(CoverScreen.isCompactWidth(340))
    }

    @Test
    fun normalPhoneWidthIsNotCompact() {
        assertFalse(CoverScreen.isCompactWidth(360))
        assertFalse(CoverScreen.isCompactWidth(411))
    }

    @Test
    fun unmeasuredWidthIsNotTreatedAsCompact() {
        assertFalse(CoverScreen.isCompactWidth(0))
        assertFalse(CoverScreen.isCompactWidth(-1))
    }
}
