### Fixed
- **Security**: Combo step slug generator strips edge separators with quantifier-free `/^-|-$/g` regex (`web/src/lib/combos/steps.ts`), eliminating polynomial-time backtracking risk on repetitive delimiter runs (closes #678, upstream `OmniRoute` commit `34e3d1f9` / CodeQL #1165 parity).
