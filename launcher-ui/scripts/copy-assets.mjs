import { cp, mkdir, rm } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const source = resolve(root, "frontend", "src");
const output = resolve(root, "frontend", "dist");

await mkdir(output, { recursive: true });
await cp(resolve(source, "index.html"), resolve(output, "index.html"));
await rm(resolve(output, "design"), { recursive: true, force: true });
await cp(resolve(source, "design"), resolve(output, "design"), { recursive: true });
