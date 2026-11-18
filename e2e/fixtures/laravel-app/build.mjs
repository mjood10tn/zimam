// What `npm run build` does here: Vite's output for one entry, without
// Vite, so the e2e run needs nothing from the npm registry. It writes
// public/build/assets/app-<hash>.js and public/build/manifest.json, as
// Laravel's Vite plugin expects them, and public/build/node.txt with the
// Node version that built it (e2e/scenarios/v04-workers.sh).
import { createHash } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';

const entry = 'resources/js/app.js';
const source = readFileSync(entry);
const file = `assets/app-${createHash('sha256').update(source).digest('hex').slice(0, 8)}.js`;
mkdirSync('public/build/assets', { recursive: true });
writeFileSync(`public/build/${file}`, source);
writeFileSync('public/build/manifest.json', `${JSON.stringify({ [entry]: { file, src: entry, isEntry: true } }, null, 2)}\n`);
writeFileSync('public/build/node.txt', `${process.version}\n`);
console.log(`built ${file} with Node ${process.version}`);
