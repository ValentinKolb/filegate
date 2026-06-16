import { themeCookieName, type AdminTheme } from "./lib/theme";

const maxAge = 60 * 60 * 24 * 365;
const lightColor = "#f2f3f5";
const darkColor = "#0f141b";

function currentTheme(): AdminTheme {
  return document.documentElement.classList.contains("dark") ? "dark" : "light";
}

function setTheme(theme: AdminTheme) {
  const root = document.documentElement;
  root.classList.toggle("dark", theme === "dark");
  root.classList.toggle("light", theme === "light");
  root.dataset.theme = theme;

  const secure = location.protocol === "https:" ? "; Secure" : "";
  document.cookie = `${themeCookieName}=${encodeURIComponent(theme)}; Path=/; Max-Age=${maxAge}; SameSite=Lax${secure}`;

  document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')?.setAttribute("content", theme === "dark" ? darkColor : lightColor);
  for (const button of document.querySelectorAll<HTMLButtonElement>("[data-theme-toggle]")) {
    button.textContent = theme === "dark" ? "Light" : "Dark";
    button.setAttribute("aria-label", `Switch to ${theme === "dark" ? "light" : "dark"} mode`);
  }
}

document.addEventListener("click", (event) => {
  const target = event.target;
  if (!(target instanceof Element) || !target.closest("[data-theme-toggle]")) return;
  setTheme(currentTheme() === "dark" ? "light" : "dark");
});

setTheme(currentTheme());
