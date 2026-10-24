export interface ParsedSearchQuery {
  raw: string;
  isEmpty: boolean;
  isRegex: boolean;
  regex: RegExp | null;
  positiveTerms: string[];
  negativeTerms: string[];
}

export function parseSearchQuery(rawQuery?: string | null): ParsedSearchQuery {
  const raw = rawQuery?.trim() ?? '';
  if (!raw) {
    return {
      raw: '',
      isEmpty: true,
      isRegex: false,
      regex: null,
      positiveTerms: [],
      negativeTerms: [],
    };
  }

  // Check if it's a regex (starts with /)
  if (raw.startsWith('/')) {
    // Try to match full regex with flags: /pattern/flags
    const fullRegexMatch = raw.match(/^\/(.+)\/([a-z]*)$/i);
    if (fullRegexMatch) {
      try {
        const flags = fullRegexMatch[2] || 'i';
        const regex = new RegExp(fullRegexMatch[1], flags);
        return {
          raw,
          isEmpty: false,
          isRegex: true,
          regex,
          positiveTerms: [],
          negativeTerms: [],
        };
      } catch {
        // Invalid regex, fall through to plain text handling
      }
    } else if (raw.length > 1) {
      // Starts with / but no closing /, treat rest as pattern with default 'i' flag
      try {
        const regex = new RegExp(raw.slice(1), 'i');
        return {
          raw,
          isEmpty: false,
          isRegex: true,
          regex,
          positiveTerms: [],
          negativeTerms: [],
        };
      } catch {
        // Invalid regex, fall through to plain text handling
      }
    }
  }

  // Split by whitespace, respecting quoted strings
  const tokens: string[] = [];
  let current = '';
  let inQuotes = false;
  let quoteChar = '';

  for (let i = 0; i < raw.length; i++) {
    const char = raw[i];
    if ((char === '"' || char === "'") && !inQuotes) {
      inQuotes = true;
      quoteChar = char;
    } else if (char === quoteChar && inQuotes) {
      inQuotes = false;
      quoteChar = '';
    } else if (char === ' ' && !inQuotes) {
      if (current) {
        tokens.push(current);
        current = '';
      }
    } else {
      current += char;
    }
  }
  if (current) tokens.push(current);

  const positiveTerms: string[] = [];
  const negativeTerms: string[] = [];

  for (const token of tokens) {
    if (token.startsWith('!') || token.startsWith('-')) {
      const term = token.slice(1).toLowerCase();
      if (term) negativeTerms.push(term);
    } else {
      positiveTerms.push(token.toLowerCase());
    }
  }

  return {
    raw,
    isEmpty: false,
    isRegex: false,
    regex: null,
    positiveTerms,
    negativeTerms,
  };
}

export function extractCandidates(target: unknown): string[] {
  if (target == null) return [];
  const candidates: string[] = [];
  
  // Handle string target directly
  if (typeof target === 'string') {
    candidates.push(target.toLowerCase());
    return candidates;
  }
  
  const obj = target as Record<string, unknown>;
  const fields = ['name', 'id', 'value', 'alias', 'provider'];
  for (const field of fields) {
    const val = obj[field];
    if (typeof val === 'string') candidates.push(val.toLowerCase());
    else if (Array.isArray(val)) {
      for (const item of val) {
        if (typeof item === 'string') candidates.push(item.toLowerCase());
      }
    }
  }
  return candidates;
}

export function matchesSearch(
  target: unknown,
  queryOrParsed?: ParsedSearchQuery | string | null
): boolean {
  const parsed =
    typeof queryOrParsed === 'string' || queryOrParsed == null
      ? parseSearchQuery(queryOrParsed)
      : queryOrParsed;

  if (parsed.isEmpty) return true;

  const candidates = extractCandidates(target);
  if (candidates.length === 0) return false;

  if (parsed.isRegex && parsed.regex) {
    return candidates.some((c) => {
      // Each candidate is an independent search, including for global/sticky regexes.
      parsed.regex!.lastIndex = 0;
      return parsed.regex!.test(c);
    });
  }

  // All positive terms must match at least one candidate
  for (const term of parsed.positiveTerms) {
    const matched = candidates.some((c) => c.includes(term));
    if (!matched) return false;
  }

  // No negative term should match any candidate
  for (const term of parsed.negativeTerms) {
    const matched = candidates.some((c) => c.includes(term));
    if (matched) return false;
  }

  return true;
}

export function filterModelsByQuery<T>(
  items: T[],
  query: string | ParsedSearchQuery
): T[] {
  const parsed = typeof query === 'string' ? parseSearchQuery(query) : query;
  return items.filter((item) => matchesSearch(item, parsed));
}
