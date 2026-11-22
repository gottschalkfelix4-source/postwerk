import DOMPurify from "dompurify";

const REMOTE_URL = /^\s*(https?:)?\/\//i;
const CSS_REMOTE_URL = /url\(\s*(['"]?)\s*(https?:)?\/\/[^)]*\)/gi;

const BASE_CSS = `
  html, body { margin: 0; padding: 0; background: #ffffff; }
  body {
    padding: 20px 24px;
    color: #18181b;
    font: 14px/1.6 ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
    overflow-wrap: anywhere;
    word-break: break-word;
  }
  img, video { max-width: 100%; height: auto; }
  img[data-blocked] { display: inline-block; min-width: 16px; min-height: 16px; background: #f4f4f5; outline: 1px dashed #d4d4d8; }
  table { max-width: 100%; }
  pre { white-space: pre-wrap; }
  a { color: #4f46e5; }
  blockquote { margin: 0 0 0 4px; padding-left: 12px; border-left: 3px solid #e4e4e7; color: #52525b; }
`;

export interface SanitizedEmail {
  doc: string;
  blocked: number;
}

/**
 * Sanitize untrusted email HTML for display in a sandboxed iframe.
 * Remote resources (images, backgrounds, fonts) are removed unless allowed.
 */
export function sanitizeEmail(html: string, allowRemote: boolean): SanitizedEmail {
  let blocked = 0;

  const hook = (node: Element) => {
    // <area> links are as clickable as <a>; both must open outside the reader.
    if (node.tagName === "A" || node.tagName === "AREA") {
      node.setAttribute("target", "_blank");
      node.setAttribute("rel", "noopener noreferrer nofollow");
    }
    if (!allowRemote) {
      for (const attr of ["src", "srcset", "background", "poster"]) {
        const v = node.getAttribute(attr);
        if (!v) continue;
        if (REMOTE_URL.test(v) || (attr === "srcset" && /https?:\/\//i.test(v))) {
          node.removeAttribute(attr);
          blocked++;
          if (node.tagName === "IMG") node.setAttribute("data-blocked", "1");
        }
      }
      const style = node.getAttribute("style");
      if (style && CSS_REMOTE_URL.test(style)) {
        CSS_REMOTE_URL.lastIndex = 0;
        node.setAttribute("style", style.replace(CSS_REMOTE_URL, "none"));
        blocked++;
      }
      CSS_REMOTE_URL.lastIndex = 0;
    }
  };

  DOMPurify.addHook("afterSanitizeAttributes", hook);
  let clean: string;
  try {
    clean = DOMPurify.sanitize(html, {
      FORCE_BODY: true,
      ADD_ATTR: ["target"],
      FORBID_TAGS: ["script", "iframe", "frame", "object", "embed", "form", "input", "button", "textarea", "select", "meta", "link", "base", "svg", "math"],
      FORBID_ATTR: ["ping", "formaction"],
      ALLOW_UNKNOWN_PROTOCOLS: false,
    });
  } finally {
    DOMPurify.removeHook("afterSanitizeAttributes");
  }

  if (!allowRemote) {
    clean = clean.replace(CSS_REMOTE_URL, () => {
      blocked++;
      return "none";
    });
  }

  const remote = allowRemote ? " https: http:" : "";
  const csp = `default-src 'none'; style-src 'unsafe-inline'; img-src data: cid:${remote}; font-src data:${remote}; media-src data:${remote}`;

  const doc = `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="${csp}"><base target="_blank"><style>${BASE_CSS}</style></head><body>${clean}</body></html>`;
  return { doc, blocked };
}
