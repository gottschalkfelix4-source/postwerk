import { cn } from "@/lib/utils";

export function LogoMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 32 32" className={cn("h-7 w-7", className)} fill="none" aria-hidden>
      <defs>
        <linearGradient id="pw-logo-grad" x1="0" y1="0" x2="32" y2="32" gradientUnits="userSpaceOnUse">
          <stop stopColor="#6366f1" />
          <stop offset="1" stopColor="#4338ca" />
        </linearGradient>
      </defs>
      <rect width="32" height="32" rx="8" fill="url(#pw-logo-grad)" />
      <rect x="7" y="9" width="18" height="14" rx="2.5" stroke="#fff" strokeWidth="2.2" />
      <path d="M7.5 11.5 16 17l8.5-5.5" stroke="#fff" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" />
      <circle cx="24.5" cy="9.5" r="3.6" fill="#c7d2fe" stroke="#4f46e5" strokeWidth="1.6" />
    </svg>
  );
}

export function Logo({ className }: { className?: string }) {
  return (
    <span className={cn("inline-flex items-center gap-2.5", className)}>
      <LogoMark />
      <span className="text-[17px] font-semibold tracking-tight text-zinc-900 dark:text-zinc-50">
        Post<span className="text-indigo-600 dark:text-indigo-400">werk</span>
      </span>
    </span>
  );
}
