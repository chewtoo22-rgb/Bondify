// Root Gradle build. See android/app/build.gradle.kts for the actual module -- this file
// only declares plugin versions shared across modules (currently just :app).
plugins {
    id("com.android.application") version "8.7.2" apply false
    id("org.jetbrains.kotlin.android") version "1.9.24" apply false
}
