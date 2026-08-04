// @ts-check
import { defineConfig } from "astro/config";
import { unified } from "@astrojs/markdown-remark";
import rehypeDocLinks from "./src/lib/rehype-doc-links.mjs";

// Custom domain (CNAME in public/) serves the site at the root path, so no
// `base` is needed. `site` powers canonical URLs, sitemap, and Open Graph tags.
export default defineConfig({
  site: "https://akari.attune.inc",
  trailingSlash: "never",
  build: {
    inlineStylesheets: "auto",
  },
  markdown: {
    // Rewrite the guide's relative `.md` links to site routes / GitHub URLs.
    // Astro 6 takes remark/rehype plugins through a `unified()` processor.
    processor: unified({ rehypePlugins: [rehypeDocLinks] }),
    // Match highlighted code to Akari's dark graphite surfaces.
    shikiConfig: {
      theme: "github-dark",
      wrap: false,
    },
  },
});
