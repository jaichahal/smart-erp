package com.smarterp.app

fun homeKind(roles: List<String>, personas: List<String>): String {
    val labels = (roles + personas).map { it.trim().lowercase() }.filter { it.isNotEmpty() }
    fun has(needle: String) = labels.any { it.contains(needle) }
    return when {
        has("cfo") || has("partner") -> "cfo"
        has("finance manager") || has("finance_manager") -> "finance-manager"
        has("accountant") -> "accountant"
        has("sales") || has("collection") -> "sales"
        else -> "default"
    }
}

fun sheetForDrag(dx: Float, dy: Float): String? {
    if (dy > 72f && kotlin.math.abs(dy) > kotlin.math.abs(dx)) return "flag"
    if (dx > 72f) return "approve"
    if (dx < -72f) return "reject"
    return null
}
