import type { APIRoute } from "astro";
import { loadGuide, isIndex, rawSlugFor } from "../lib/guide";

// llms.txt (https://llmstxt.org): a curated, machine-readable index of the
// docs so an agent can discover the guide and fetch any page as Markdown in one
// hop. Each link points at the raw `.md` representation served from /guide.
const SITE = "https://akari.attune.inc";

export const GET: APIRoute = async () => {
  const entries = await loadGuide();
  const index = entries.find(isIndex);
  const chapters = entries.filter((e) => !isIndex(e));

  const out: string[] = [];
  out.push("# akari");
  out.push("");
  out.push(
    "> One searchable history of every AI coding-agent session across your fleet, self-hosted."
  );
  out.push("");
  out.push(
    "akari collects the local session logs of Claude Code, Codex, pi, Cursor, and Grok from every machine, parses them on one server, and shows them as a searchable history grouped by git project, with token usage and cost on every run. This is the user guide."
  );
  out.push("");
  out.push("## User guide");
  out.push("");
  if (index) {
    out.push(`- [${index.data.title}](${SITE}/guide/index.md): ${index.data.summary}`);
  }
  for (const e of chapters) {
    out.push(`- [${e.data.title}](${SITE}/guide/${rawSlugFor(e)}.md): ${e.data.summary}`);
  }
  out.push("");
  out.push("## Optional");
  out.push("");
  out.push(`- [Full guide as one file](${SITE}/llms-full.txt): every chapter concatenated for a single fetch.`);
  out.push(`- [Source repository](https://github.com/jssblck/akari): the server, the client, and the engineering design.`);
  out.push("");

  return new Response(out.join("\n"), {
    headers: { "Content-Type": "text/plain; charset=utf-8" },
  });
};
