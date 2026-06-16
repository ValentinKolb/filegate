export type AdminTheme = "light" | "dark";

export const themeCookieName = "filegate_admin_theme";

function isTheme(value: string): value is AdminTheme {
  return value === "light" || value === "dark";
}

function decodeCookie(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

export function readThemeFromCookieHeader(cookieHeader: string | null | undefined): AdminTheme {
  let theme: AdminTheme = "light";
  for (const part of (cookieHeader ?? "").split(";")) {
    const trimmed = part.trim();
    const separator = trimmed.indexOf("=");
    if (separator <= 0 || trimmed.slice(0, separator) !== themeCookieName) continue;
    const value = decodeCookie(trimmed.slice(separator + 1));
    if (isTheme(value)) theme = value;
  }
  return theme;
}
