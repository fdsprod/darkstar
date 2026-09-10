import { API } from 'typescript/unstable/sync';
import { SyntaxKind, isStatement, isFunctionLikeDeclaration } from 'typescript/unstable/ast';
import { readdir } from 'node:fs/promises';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../../../', import.meta.url));

/** Inspect the original TypeScript AST so comments and strings cannot hide or
 * create violations. For-loop header separators are not statement boundaries. */
export function lintSourceFile(source) {
  const violations = [];
  const statementsByLine = new Map();
  const line = position => source.getLineAndCharacterOfPosition(position).line + 1;
  const report = (node, message) => {
    violations.push({ line: line(node.getStart(source)), message });
  };
  const checkBody = body => {
    if (!body) {
      return;
    }
    if (body.kind !== SyntaxKind.Block) {
      report(body, 'Control flow and function bodies require braces.');
      return;
    }
    if (line(body.getStart(source)) === line(body.end - 1)) {
      report(body, 'Braced bodies must span multiple lines.');
    }
    for (const statement of body.statements) {
      if (line(statement.getStart(source)) === line(body.getStart(source)) || line(statement.end - 1) === line(body.end - 1)) {
        report(statement, 'Put each operation on its own line inside braces.');
      }
    }
  };
  const visit = node => {
    if (node.kind === SyntaxKind.IfStatement) {
      checkBody(node.thenStatement);
      checkBody(node.elseStatement);
    } else if ([SyntaxKind.ForStatement, SyntaxKind.ForInStatement, SyntaxKind.ForOfStatement, SyntaxKind.WhileStatement, SyntaxKind.DoStatement, SyntaxKind.WithStatement].includes(node.kind)) {
      checkBody(node.statement);
    } else if (isFunctionLikeDeclaration(node) && node.body) {
      checkBody(node.body);
    } else if (node.kind === SyntaxKind.TryStatement) {
      checkBody(node.tryBlock);
      checkBody(node.catchClause?.block);
      checkBody(node.finallyBlock);
    } else if (node.kind === SyntaxKind.CaseClause || node.kind === SyntaxKind.DefaultClause) {
      for (const statement of node.statements) {
        if (line(statement.getStart(source)) === line(node.getStart(source))) {
          report(statement, 'Put switch case operations below the case label.');
        }
      }
    }
    if (isStatement(node) && node.kind !== SyntaxKind.Block && node.kind !== SyntaxKind.EmptyStatement) {
      const startLine = line(node.getStart(source));
      const previous = statementsByLine.get(startLine);
      if (previous) {
        report(node, 'Put each statement on its own line.');
      }
      statementsByLine.set(startLine, node);
    }
    if (node.kind === SyntaxKind.VariableDeclarationList && node.declarations.length > 1) {
      report(node, 'Declare one variable per statement.');
    }
    if (node.kind === SyntaxKind.BinaryExpression && node.operatorToken.kind === SyntaxKind.CommaToken) {
      report(node, 'Sequence operations require separate statements.');
    }
    node.forEachChild(visit);
  };
  visit(source);
  return violations;
}

export function lintFiles(files) {
  const api = new API({ cwd: root });
  try {
    const absolute = files.map(file => resolve(file));
    const snapshot = api.updateSnapshot({ openFiles: absolute });
    try {
      return absolute.flatMap(file => {
        const source = snapshot.getDefaultProjectForFile(file)?.program.getSourceFile(file);
        if (!source) {
          throw new Error(`TypeScript did not load ${file}`);
        }
        return lintSourceFile(source).map(violation => ({ file, ...violation }));
      });
    } finally {
      snapshot.dispose();
    }
  } finally {
    api.close();
  }
}

async function sourceFiles(directory) {
  const files = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const path = resolve(directory, entry.name);
    if (entry.isDirectory()) {
      files.push(...await sourceFiles(path));
    } else if (entry.name.endsWith('.ts')) {
      files.push(path);
    }
  }
  return files;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const files = await sourceFiles(resolve(root, 'packages/plugin-sdk/src'));
  for (const entry of await readdir(resolve(root, 'plugins'), { withFileTypes: true })) {
    if (!entry.isDirectory()) {
      continue;
    }
    try {
      files.push(...await sourceFiles(resolve(root, 'plugins', entry.name, 'src')));
    } catch (error) {
      if (error.code !== 'ENOENT') {
        throw error;
      }
    }
  }
  const violations = lintFiles(files);
  for (const violation of violations) {
    console.error(`${violation.file}:${violation.line}: ${violation.message}`);
  }
  if (violations.length) {
    process.exitCode = 1;
  } else {
    console.log(`Plugin control-flow lint passed (${files.length} TypeScript files).`);
  }
}

