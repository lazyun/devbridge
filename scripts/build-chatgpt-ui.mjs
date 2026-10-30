#!/usr/bin/env node
// Offline build validation for self-contained MCP App resources. The explicit
// output directory keeps build artifacts out of source control.
import {mkdir, readFile, readdir, writeFile} from 'node:fs/promises';
import {createHash} from 'node:crypto';
import {basename, resolve} from 'node:path';
import vm from 'node:vm';

const output = process.argv[2];
if (!output) throw new Error('usage: node scripts/build-chatgpt-ui.mjs <output-directory>');
const sources = [
  'internal/mcpserver/dev_bridge_ui.html',
  'internal/mcpserver/feature_area_details_ui.html',
];
const target = resolve(output);
await mkdir(target, {recursive: true});
if ((await readdir(target)).length) throw new Error('output directory must be empty; existing files are never removed');
const manifest = [];
for (const source of sources) {
  const html = await readFile(source, 'utf8');
  if (!/^<!doctype html>/i.test(html) || !html.includes('</html>')) throw new Error(`${source}: not a complete HTML document`);
  const scripts = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)];
  if (!scripts.length) throw new Error(`${source}: expected at least one embedded script`);
  scripts.forEach((script, index) => {
    new vm.Script(script[1], {filename: `${source}#script-${index + 1}`});
  });
  if (/<script\s+src=|<link[^>]+href=/i.test(html)) throw new Error(`${source}: resource must remain self-contained`);
  await writeFile(resolve(target, basename(source)), html);
  manifest.push({asset: basename(source), sha256: createHash('sha256').update(html).digest('hex')});
}
await writeFile(resolve(target, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n');
console.log(`validated ${sources.length} self-contained ChatGPT UI assets in ${target}`);
