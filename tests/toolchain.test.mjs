import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, writeFile, copyFile, rm, realpath } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve, relative, isAbsolute, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const repository = resolve(dirname(fileURLToPath(import.meta.url)), '..');

test('PowerShell entrypoints override hostile PATH and reject changed pins without fallback', {skip:process.platform !== 'win32'}, async () => {
  const binding = await readFile(join(repository,'.darkstar/toolchains.json'),'utf8');
  const root = await mkdtemp(join(tmpdir(),'darkstar-toolchain-test-'));
  try {
    await mkdir(join(root,'scripts')); await mkdir(join(root,'.darkstar')); await mkdir(join(root,'hostile'));
    for (const script of ['Use-ProjectToolchain.ps1','Assert-Toolchain.ps1']) await copyFile(join(repository,'scripts',script),join(root,'scripts',script));
    for (const pin of ['.go-version','.node-version','.npm-version']) await copyFile(join(repository,pin),join(root,pin));
    await writeFile(join(root,'.darkstar/toolchains.json'),binding);
    for (const command of ['go','node','npm','npx']) await writeFile(join(root,'hostile',command+'.cmd'),'@echo Wrong host tool\r\n@exit /b 1\r\n');
    const env={...process.env, PATH:join(root,'hostile')+';'+process.env.PATH, GOROOT:join(root,'wrong-go'), GOTOOLCHAIN:'auto'};
    const check=()=>spawnSync('pwsh',['-NoProfile','-File',join(root,'scripts/Assert-Toolchain.ps1')],{env,encoding:'utf8',windowsHide:true,timeout:30000});
    const passed=check(); assert.equal(passed.status,0,passed.stdout+'\n'+passed.stderr);
    assert.match(passed.stdout,/Toolchain verified/);
    for (const pin of ['.go-version','.node-version','.npm-version']) await writeFile(join(root,pin),'1.0.0\n');
    const newer=check(); assert.equal(newer.status,0,newer.stdout+'\n'+newer.stderr);
    await writeFile(join(root,'.go-version'),'99.0.0\n');
    const failed=check(); assert.notEqual(failed.status,0); assert.match(failed.stdout+failed.stderr,/Go 99\.0\.0 or newer is required/);
  } finally {
    const resolvedRoot=await realpath(root), rel=relative(await realpath(tmpdir()),resolvedRoot);
    assert.ok(rel && !isAbsolute(rel) && !rel.startsWith('..'));
    await rm(resolvedRoot,{recursive:true,force:true});
  }
});
