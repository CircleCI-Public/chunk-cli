#!/usr/bin/env bun
import { existsSync, statSync, mkdirSync, rmSync } from 'node:fs';
import { join } from 'node:path';

const TARGETS = [
  { name: 'darwin-arm64', target: 'bun-darwin-arm64' },
  { name: 'darwin-x64', target: 'bun-darwin-x64' },
  { name: 'linux-arm64', target: 'bun-linux-arm64' },
  { name: 'linux-x64', target: 'bun-linux-x64' },
] as const;

const MAX_SIZE_MB = 50;
const MAX_STARTUP_MS = 500;

interface BuildResult {
  platform: string;
  outputPath: string;
  sizeMB: number;
  success: boolean;
  error?: string;
}

async function build(): Promise<void> {
  const rootDir = join(import.meta.dir, '..');
  const distDir = join(rootDir, 'dist');
  const entryPoint = join(rootDir, 'src', 'index.ts');

  console.log('🔨 Building chunk binaries...\n');

  if (existsSync(distDir)) {
    rmSync(distDir, { recursive: true });
  }
  mkdirSync(distDir, { recursive: true });

  const results: BuildResult[] = [];

  for (const { name, target } of TARGETS) {
    const outputPath = join(distDir, `chunk-${name}`);
    console.log(`📦 Building ${name}...`);

    try {
      const proc = Bun.spawn([
        'bun', 'build', entryPoint,
        '--compile',
        '--minify',
        `--target=${target}`,
        '--loader', '.md:text',
        `--outfile=${outputPath}`,
      ], {
        cwd: rootDir,
        stdout: 'pipe',
        stderr: 'pipe',
      });

      const exitCode = await proc.exited;
      
      if (exitCode !== 0) {
        const stderr = await new Response(proc.stderr).text();
        results.push({
          platform: name,
          outputPath,
          sizeMB: 0,
          success: false,
          error: stderr || `Build failed with exit code ${exitCode}`,
        });
        continue;
      }

      if (!existsSync(outputPath)) {
        results.push({
          platform: name,
          outputPath,
          sizeMB: 0,
          success: false,
          error: 'Binary not created',
        });
        continue;
      }

      const stats = statSync(outputPath);
      const sizeMB = stats.size / (1024 * 1024);

      results.push({
        platform: name,
        outputPath,
        sizeMB,
        success: true,
      });

      console.log(`   ✓ ${name}: ${sizeMB.toFixed(1)} MB`);
    } catch (err) {
      results.push({
        platform: name,
        outputPath,
        sizeMB: 0,
        success: false,
        error: err instanceof Error ? err.message : String(err),
      });
    }
  }

  console.log('\n📊 Build Summary:');
  console.log('─'.repeat(50));

  let allSuccess = true;
  let sizeWarnings = false;

  for (const result of results) {
    if (!result.success) {
      console.log(`❌ ${result.platform}: FAILED - ${result.error}`);
      allSuccess = false;
    } else {
      const sizeStatus = result.sizeMB <= MAX_SIZE_MB ? '✓' : '⚠️';
      if (result.sizeMB > MAX_SIZE_MB) {
        sizeWarnings = true;
      }
      console.log(`${sizeStatus} ${result.platform}: ${result.sizeMB.toFixed(1)} MB`);
    }
  }

  console.log('─'.repeat(50));

  if (sizeWarnings) {
    console.log(`⚠️  Warning: Some binaries exceed ${MAX_SIZE_MB} MB target`);
  }

  if (!allSuccess) {
    console.log('\n❌ Build failed');
    process.exit(1);
  }

  const currentPlatform = process.platform === 'darwin'
    ? `darwin-${process.arch === 'arm64' ? 'arm64' : 'x64'}`
    : `linux-${process.arch === 'arm64' ? 'arm64' : 'x64'}`;
  const currentBinary = results.find(r => r.platform === currentPlatform && r.success);

  if (currentBinary) {
    console.log(`\n⏱️  Testing startup time for ${currentPlatform}...`);
    
    // Run multiple times to get a warm cache measurement
    // First run may be slower due to disk I/O
    const times: number[] = [];
    for (let i = 0; i < 5; i++) {
      const start = performance.now();
      const testProc = Bun.spawn([currentBinary.outputPath, '--version'], {
        stdout: 'pipe',
        stderr: 'pipe',
      });
      await testProc.exited;
      times.push(performance.now() - start);
    }
    
    // Use the median time (more representative than average with outliers)
    times.sort((a, b) => a - b);
    const median = times[Math.floor(times.length / 2)] ?? 0;

    const startupStatus = median <= MAX_STARTUP_MS ? '✓' : '⚠️';
    console.log(`${startupStatus} Startup time: ${median.toFixed(0)} ms median (target: <${MAX_STARTUP_MS} ms)`);

    if (median > MAX_STARTUP_MS) {
      console.log(`⚠️  Warning: Startup time exceeds ${MAX_STARTUP_MS} ms target`);
    }
  }

  console.log('\n✅ Build complete!');
  console.log(`   Binaries: ${distDir}/`);
}

build().catch((err) => {
  console.error('Build error:', err);
  process.exit(1);
});
