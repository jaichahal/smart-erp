package com.smarterp.app

import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.ui.graphics.Color

private val Ink = Color(0xFF0F172A)
private val Paper = Color(0xFFFFFFFF)
private val Night = Color(0xFF000000)
private val Emerald = Color(0xFF047857)
private val EmeraldOnDark = Color(0xFF6EE7B7)

fun lightScheme() = lightColorScheme(
    background = Paper,
    onBackground = Ink,
    surface = Paper,
    onSurface = Ink,
    primary = Emerald,
    onPrimary = Paper,
)

fun darkScheme() = darkColorScheme(
    background = Night,
    onBackground = Paper,
    surface = Night,
    onSurface = Paper,
    primary = EmeraldOnDark,
    onPrimary = Night,
)
