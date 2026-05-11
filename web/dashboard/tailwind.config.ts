import type { Config } from "tailwindcss";

export default {
  darkMode: ["selector", '[data-theme="dark"]'],
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        border: "var(--border)",
        input: "var(--border)",
        ring: "var(--primary)",
        background: "var(--bg)",
        foreground: "var(--text)",
        primary: {
          DEFAULT: "var(--primary)",
          foreground: "#06110f"
        },
        secondary: {
          DEFAULT: "var(--surface-2)",
          foreground: "var(--text)"
        },
        muted: {
          DEFAULT: "var(--surface-2)",
          foreground: "var(--muted)"
        },
        destructive: {
          DEFAULT: "var(--bad)",
          foreground: "#ffffff"
        },
        card: {
          DEFAULT: "var(--panel-bg)",
          foreground: "var(--text)"
        }
      },
      borderRadius: {
        lg: "8px",
        md: "6px",
        sm: "4px"
      }
    }
  },
  plugins: []
} satisfies Config;
