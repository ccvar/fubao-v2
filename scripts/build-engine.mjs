import { execFileSync } from "node:child_process";
import { mkdirSync, renameSync, rmSync } from "node:fs";
import { join, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const binaryDir = join(root, "src-tauri", "binaries");
const target = execFileSync("rustc", ["--print", "host-tuple"], {
  encoding: "utf8",
}).trim();

const mappings = {
  "aarch64-apple-darwin": { goos: "darwin", goarch: "arm64", suffix: "" },
  "x86_64-apple-darwin": { goos: "darwin", goarch: "amd64", suffix: "" },
  "x86_64-pc-windows-msvc": { goos: "windows", goarch: "amd64", suffix: ".exe" },
  "aarch64-pc-windows-msvc": { goos: "windows", goarch: "arm64", suffix: ".exe" },
  "x86_64-unknown-linux-gnu": { goos: "linux", goarch: "amd64", suffix: "" },
  "aarch64-unknown-linux-gnu": { goos: "linux", goarch: "arm64", suffix: "" },
};

const platform = mappings[target];
if (!platform) {
  throw new Error(`Unsupported Rust target tuple: ${target}`);
}

mkdirSync(binaryDir, { recursive: true });
const tempOutput = join(binaryDir, `fubao-engine-build${platform.suffix}`);
const finalOutput = join(binaryDir, `fubao-engine-${target}${platform.suffix}`);
rmSync(tempOutput, { force: true });

execFileSync(
  "go",
  ["build", "-trimpath", "-ldflags=-s -w", "-o", tempOutput, "./cmd/fubao-engine"],
  {
    cwd: join(root, "engine"),
    env: {
      ...process.env,
      GOOS: platform.goos,
      GOARCH: platform.goarch,
      CGO_ENABLED: "0",
      GOCACHE: join(root, ".cache", "go-build"),
    },
    stdio: "inherit",
  },
);

rmSync(finalOutput, { force: true });
renameSync(tempOutput, finalOutput);
console.log(`Go sidecar ready: ${finalOutput}`);
