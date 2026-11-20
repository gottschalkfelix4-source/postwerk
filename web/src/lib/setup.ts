const DEFER_KEY = "postwerk.setup.deferred";

/** True once the admin was sent to the setup wizard (or skipped it) in this browser session. */
export function isSetupDeferred(): boolean {
  try {
    return window.sessionStorage.getItem(DEFER_KEY) === "1";
  } catch {
    return false;
  }
}

export function deferSetup(): void {
  try {
    window.sessionStorage.setItem(DEFER_KEY, "1");
  } catch {
    // Storage may be unavailable (private mode, blocked site data).
  }
}

/** Suggest the mail domain for a host name: "mail.example.com" → "example.com". */
export function domainFromHostname(hostname: string): string {
  const parts = hostname.trim().toLowerCase().replace(/\.$/, "").split(".").filter(Boolean);
  if (parts.length >= 3) return parts.slice(1).join(".");
  return parts.join(".");
}

export interface PasswordStrength {
  score: 0 | 1 | 2 | 3 | 4;
  label: string;
}

/** Rough password strength estimate for the strength meter. */
export function passwordStrength(pw: string): PasswordStrength {
  if (!pw) return { score: 0, label: "" };
  let points = 0;
  if (pw.length >= 10) points++;
  if (pw.length >= 14) points++;
  if (pw.length >= 20) points++;
  const classes = [/[a-z]/, /[A-Z]/, /\d/, /[^A-Za-z0-9]/].filter((re) => re.test(pw)).length;
  if (classes >= 3) points++;
  if (classes === 4) points++;
  if (/^(.)\1+$/.test(pw) || /^(?:1234|password|passwort|qwert)/i.test(pw)) points = 0;
  if (pw.length < 10) points = Math.min(points, 1);
  const score = Math.min(4, points) as PasswordStrength["score"];
  const labels = ["Sehr schwach", "Schwach", "Mittel", "Gut", "Stark"];
  return { score, label: labels[score] };
}
