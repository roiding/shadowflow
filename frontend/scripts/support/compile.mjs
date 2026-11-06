import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import ts from 'typescript'

// Transpiles src TS modules into a temp dir under frontend/scripts/ so bare
// npm imports resolve; data: URL imports cannot resolve node_modules.
export function compileModules(specs) {
  const dir = mkdtempSync(path.join(fileURLToPath(new URL('../', import.meta.url)), '.compile-'))
  const names = specs.map((spec) => path.basename(fileURLToPath(spec)).replace(/\.ts$/, ''))
  const urls = {}
  try {
    specs.forEach((spec, index) => {
      const compiled = ts.transpileModule(readFileSync(spec, 'utf8'), {
        compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
      }).outputText
      const rewritten = names.reduce((text, name, other) => other === index ? text : text.replaceAll(`from './${name}'`, `from './${name}.mjs'`), compiled)
      const file = path.join(dir, `${names[index]}.mjs`)
      writeFileSync(file, rewritten)
      urls[names[index]] = pathToFileURL(file).href
    })
  } catch (error) {
    rmSync(dir, { recursive: true, force: true })
    throw error
  }
  return { urls, cleanup: () => rmSync(dir, { recursive: true, force: true }) }
}
