import React from "react";

// Declarative glyph set (no icon dependency). Renderers are static
// identities, so icons may key off renderer ids — never off plugin data.
interface IconProps {
  size?: number;
}

function svg(path: React.ReactNode) {
  return function Icon({ size = 15 }: IconProps) {
    return (
      <svg
        width={size}
        height={size}
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.8"
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden="true"
      >
        {path}
      </svg>
    );
  };
}

export const LayersIcon = svg(
  <>
    <path d="M12 3 3 8l9 5 9-5-9-5Z" />
    <path d="m3 13 9 5 9-5" />
  </>
);

export const GridIcon = svg(
  <>
    <rect x="3.5" y="3.5" width="7" height="7" rx="1.5" />
    <rect x="13.5" y="3.5" width="7" height="7" rx="1.5" />
    <rect x="3.5" y="13.5" width="7" height="7" rx="1.5" />
    <rect x="13.5" y="13.5" width="7" height="7" rx="1.5" />
  </>
);

export const FolderIcon = svg(
  <path d="M3.5 7a2 2 0 0 1 2-2h4l2 2.5h7a2 2 0 0 1 2 2V17a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2V7Z" />
);

export const PlugIcon = svg(
  <>
    <path d="M9 3v5" />
    <path d="M15 3v5" />
    <path d="M6.5 8h11v3a5.5 5.5 0 0 1-11 0V8Z" />
    <path d="M12 16.5V21" />
  </>
);

export const PulseIcon = svg(<path d="M3 12h4l2.5-6 4 12 2.5-6h5" />);

export const TagIcon = svg(
  <>
    <path d="M3.5 11.5v-7a1 1 0 0 1 1-1h7L21 13l-8 8-9.5-9.5Z" />
    <circle cx="8" cy="8" r="1.4" />
  </>
);

export const PlayIcon = svg(<path d="M7 4.8v14.4L19 12 7 4.8Z" />);

export const LogoMark = svg(
  <>
    <path d="M12 3 4 7.5v9L12 21l8-4.5v-9L12 3Z" />
    <path d="M12 12 4 7.5" />
    <path d="m12 12 8-4.5" />
    <path d="M12 12v9" />
  </>
);

// Renderer identity -> glyph. Unknown renderers fall back to a neutral
// mark because composition is open-ended.
const rendererIcons: Record<string, (p: IconProps) => React.ReactElement> = {
  dashboard: GridIcon,
  collections: LayersIcon,
  files: FolderIcon,
  sources: PlugIcon,
  "plugin-explorer": PulseIcon,
};

export function RendererIcon({ renderer }: { renderer: string }) {
  const Icon = rendererIcons[renderer] ?? TagIcon;
  return <Icon />;
}
