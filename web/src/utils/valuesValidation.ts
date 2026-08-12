import type { SecretOption, SecretRefFormItem, ValidationIssue } from '@/types/valuesRevision';
import { canonicalize } from './valuesCanonical';

const secretKeyPattern = /(password|passwd|secret|token|api[_-]?key|private[_-]?key|client[_-]?secret|access[_-]?key)/i;
const secretValuePattern = /^(?:AKIA[A-Z0-9]{16}|[A-Za-z0-9+/]{24,}={0,2}|gh[pousr]_[A-Za-z0-9_]{20,}|sk-[A-Za-z0-9_-]{20,})$/;
const referencePattern = /^(?:\$\{|ref\s|\{\{|<ref>|\[ref\]|null$|~$)/i;

export function detectSecretLiteral(document: string): ValidationIssue | null {
  const lines = document.split(/\r?\n/);
  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index];
    const fieldMatch = line.match(/^\s*["']?([\w.-]+)["']?\s*:\s*(.+?)\s*(?:#.*)?$/);
    if (!fieldMatch) continue;
    const [, field, rawValue] = fieldMatch;
    const value = rawValue.replace(/,$/, '').trim().replace(/^(["'])(.*)\1$/, '$2').trim();
    if (value === '' || referencePattern.test(value)) continue;
    if (secretKeyPattern.test(field) || secretValuePattern.test(value)) {
      return { code: 'secret_literal_forbidden', message: `第 ${index + 1} 行疑似包含 Secret 明文，请使用 SecretRef 引用`, line: index + 1 };
    }
  }
  return null;
}

export function validateValuesDocument(document: string): { canonical: ReturnType<typeof canonicalize>; issue: ValidationIssue | null } {
  const canonical = canonicalize(document);
  if (canonical.issue) return { canonical, issue: canonical.issue };
  const secretIssue = detectSecretLiteral(document);
  return { canonical, issue: secretIssue };
}

export function validateSecretRefs(items: SecretRefFormItem[], available: SecretOption[]): string | null {
  for (const item of items) {
    if (!(item.path ?? '').trim() || !item.name.trim() || !item.key.trim()) return '请完成 SecretRef 配置';
    const option = available.find((candidate) => candidate.name === item.name);
    if (!option || !option.keys.includes(item.key)) return '请选择有效的 Secret 名称和 Key';
  }
  return null;
}

export function suggestedSecretPath(name: string, key: string): string {
  const segment = (value: string) => value.replace(/[^A-Za-z0-9_]/g, '_');
  return name && key ? `.secrets.${segment(name)}.${segment(key)}` : '';
}
