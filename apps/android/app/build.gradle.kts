plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.plugin.compose")
}

// Host port published by the api service in deploy/compose/docker-compose.yml.
// The emulator reaches the Mac through 10.0.2.2.
val baselineApiHostPort: String = run {
    val compose = rootProject.file("../../deploy/compose/docker-compose.yml")
    val text = compose.readText()
    val api = text.substringAfter("\n  api:\n").substringBefore("\n  worker:\n")
    val match = Regex(""":(\d+):8080""").find(api)
        ?: error("API host port not found under service api in ${compose.path}")
    match.groupValues[1]
}

android {
    namespace = "com.smarterp.app"
    compileSdk {
        version = release(37) {
            minorApiLevel = 0
        }
    }

    defaultConfig {
        applicationId = "com.smarterp.app"
        minSdk = 26
        targetSdk = 34
        versionCode = 1
        versionName = "0.1.0"
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        buildConfigField(
            "String",
            "API_BASE_URL",
            "\"http://10.0.2.2:$baselineApiHostPort\"",
        )
    }

    buildFeatures {
        compose = true
        buildConfig = true
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    packaging {
        resources {
            excludes += "/META-INF/{AL2.0,LGPL2.1}"
        }
    }
}

dependencies {
    val composeBom = platform("androidx.compose:compose-bom:2026.09.00")
    implementation(composeBom)
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.material3:material3")
    implementation("androidx.activity:activity-compose:1.13.0")

    androidTestImplementation(composeBom)
    androidTestImplementation("androidx.compose.ui:ui-test-junit4")
    androidTestImplementation("androidx.test.ext:junit:1.2.1")
    androidTestImplementation("androidx.test.espresso:espresso-core:3.6.1")
    debugImplementation("androidx.compose.ui:ui-test-manifest")
}
