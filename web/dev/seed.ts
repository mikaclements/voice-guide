import { existsSync } from "node:fs";
import { join } from "node:path";

// The path of a user file: the developer's own in local when it exists, else the tracked sample in
// sample. Each file is chosen on its own, so one own file can sit beside the other's sample.
export function seedFile(sample: string, local: string | undefined, file: string): string {
  const own = local && join(local, file);
  return own && existsSync(own) ? own : join(sample, file);
}
