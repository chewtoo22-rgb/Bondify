import org.gradle.api.tasks.Exec

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "dev.bondify.app"
    compileSdk = 34

    defaultConfig {
        applicationId = "dev.bondify.app"
        // Matches mobile/build_aar.sh's `gomobile bind -androidapi 24`: VpnService.Builder's
        // modern per-Network APIs (setUnderlyingNetworks) and ConnectivityManager's
        // requestNetwork callback API both need this floor.
        minSdk = 24
        targetSdk = 34
        versionCode = 1
        versionName = "0.1.0"
    }

    buildTypes {
        release {
            isMinifyEnabled = false
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_1_8
        targetCompatibility = JavaVersion.VERSION_1_8
    }
    kotlinOptions {
        jvmTarget = "1.8"
    }
}

dependencies {
    implementation(files("libs/bondifymobile.aar"))
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("com.google.android.material:material:1.12.0")
    implementation("androidx.constraintlayout:constraintlayout:2.1.4")
}

// bondifymobile.aar is generated, not committed (it bundles ~14MB of per-ABI native
// libraries built from core/bond et al. -- see mobile/build_aar.sh's doc comment for why
// this lives outside Gradle as a standalone script). Building it needs the Android NDK
// (gomobile bind's cgo cross-compilation) on top of the SDK this module already needs, so
// it's skipped with a clear failure message rather than silently producing a broken build
// if ANDROID_NDK_HOME isn't set -- matching this repo's established pattern of never
// papering over an unmet real-hardware/toolchain dependency (see testbed/run_phase4.sh's
// xt_statistic probe for the same idea applied to a test gate instead of a build step).
val generateMobileAar = tasks.register<Exec>("generateMobileAar") {
    val aarFile = file("libs/bondifymobile.aar")
    val mobileSrcDir = file("../../mobile")
    inputs.dir(mobileSrcDir)
    outputs.file(aarFile)
    workingDir = rootDir
    commandLine("bash", "../mobile/build_aar.sh")
    doFirst {
        if (System.getenv("ANDROID_NDK_HOME").isNullOrEmpty()) {
            throw GradleException(
                "ANDROID_NDK_HOME is not set -- required to build bondifymobile.aar via " +
                    "gomobile bind (see mobile/build_aar.sh). Install the NDK and set " +
                    "ANDROID_NDK_HOME, or place a prebuilt bondifymobile.aar at " +
                    "android/app/libs/bondifymobile.aar yourself to skip this step."
            )
        }
    }
}

tasks.named("preBuild") {
    dependsOn(generateMobileAar)
}
