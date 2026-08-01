import { execFileSync } from "node:child_process";
import { mkdirSync, renameSync, rmSync } from "node:fs";
import { join, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const binaryDir = join(root, "src-tauri", "binaries");
const hostTarget = execFileSync("rustc", ["--print", "host-tuple"], {
  encoding: "utf8",
}).trim();
const target =
  process.env.TAURI_ENV_TARGET_TRIPLE ||
  process.env.CARGO_BUILD_TARGET ||
  hostTarget;

const mappings = {
  "aarch64-apple-darwin": { goos: "darwin", goarch: "arm64", suffix: "" },
  "x86_64-apple-darwin": { goos: "darwin", goarch: "amd64", suffix: "" },
  "x86_64-pc-windows-msvc": { goos: "windows", goarch: "amd64", suffix: ".exe" },
  "aarch64-pc-windows-msvc": { goos: "windows", goarch: "arm64", suffix: ".exe" },
  "x86_64-unknown-linux-gnu": { goos: "linux", goarch: "amd64", suffix: "" },
  "aarch64-unknown-linux-gnu": { goos: "linux", goarch: "arm64", suffix: "" },
};

const platform = mappings[target];
if (!platform && target !== "universal-apple-darwin") {
  throw new Error(`Unsupported Rust target tuple: ${target}`);
}

mkdirSync(binaryDir, { recursive: true });

function buildGo(output, buildPlatform) {
  rmSync(output, { force: true });
  execFileSync(
    "go",
    ["build", "-trimpath", "-ldflags=-s -w", "-o", output, "./cmd/fubao-engine"],
    {
      cwd: join(root, "engine"),
      env: {
        ...process.env,
        GOOS: buildPlatform.goos,
        GOARCH: buildPlatform.goarch,
        CGO_ENABLED: "0",
        GOCACHE: join(root, ".cache", "go-build"),
      },
      stdio: "inherit",
    },
  );
}

if (target === "universal-apple-darwin") {
  const amd64Output = join(
    binaryDir,
    "fubao-engine-x86_64-apple-darwin",
  );
  const arm64Output = join(
    binaryDir,
    "fubao-engine-aarch64-apple-darwin",
  );
  const finalOutput = join(binaryDir, `fubao-engine-${target}`);

  buildGo(amd64Output, mappings["x86_64-apple-darwin"]);
  buildGo(arm64Output, mappings["aarch64-apple-darwin"]);
  rmSync(finalOutput, { force: true });
  execFileSync(
    "lipo",
    ["-create", "-output", finalOutput, amd64Output, arm64Output],
    { stdio: "inherit" },
  );
  console.log(
    `Universal Go sidecars ready: ${amd64Output}, ${arm64Output}, ${finalOutput}`,
  );
} else {
  const tempOutput = join(binaryDir, `fubao-engine-build${platform.suffix}`);
  const finalOutput = join(binaryDir, `fubao-engine-${target}${platform.suffix}`);
  buildGo(tempOutput, platform);
  rmSync(finalOutput, { force: true });
  renameSync(tempOutput, finalOutput);
  console.log(`Go sidecar ready: ${finalOutput}`);
}
