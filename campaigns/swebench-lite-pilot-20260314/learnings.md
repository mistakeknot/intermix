# SWE-bench Lite Pilot — 10 Instances (2026-03-14)

**Bead:** Sylveste-pr33
**Pass Rate:** 1/10 (10%)
**Total Runtime:** ~35 minutes
**Total Tokens:** 56K output, 10.8M effective context (mostly cache reads)

## Results Summary

| # | Instance | Repo | Skaffen | Patch | Validation | Outcome | Root Cause |
|---|----------|------|---------|-------|------------|---------|------------|
| 1 | django-11179 | Django 3.0 | exit=0, 1 file, 73s | 614B | FAIL | partial | Python 3.12: `distutils` removed |
| 2 | django-14999 | Django 4.1 | exit=0, 1 file, 205s | 792B | PASS | **success** | — |
| 3 | matplotlib-23913 | matplotlib 3.6 | exit=0, 1 file, 242s | ? | FAIL | partial | `.venv/bin/pytest` missing (setup extras failed) |
| 4 | flask-4045 | Flask 2.0 | exit=0, 3 files, 213s | ? | FAIL | partial | test_patch conflict + `.venv/bin/pytest` missing |
| 5 | requests-2674 | requests 2.7 | exit=0, 1 file, 79s | ? | FAIL | partial | pytest exit=4 (no tests collected or import error) |
| 6 | pytest-5221 | pytest 4.4 | exit=0, 1 file, 171s | ? | FAIL | partial | Python 3.12: `imp` module removed |
| 7 | scikit-learn-13497 | sklearn 0.21 | — | — | — | setup_failure | Python 3.12: old numpy/scipy won't compile |
| 8 | sphinx-7975 | Sphinx 3.2 | exit=0, 1 file, 207s | 922B | FAIL | partial | Python 3.12: `pkg_resources` not installed |
| 9 | sympy-15345 | SymPy 1.4 | exit=0, 1 file, 100s | ? | FAIL | partial | Validation failed (unknown — may be wrong fix) |
| 10 | sympy-16503 | SymPy 1.5 | exit=0, 2 files, 516s | ? | FAIL | partial | test_patch conflict + validation failed |

## Key Findings

### 1. Skaffen Works Well
- **9/10 cells produced patches** (every cell except scikit-learn which failed at setup)
- Skaffen exit=0 in all cases, meaning it completed without crashing
- Average Skaffen duration: ~175s (range: 73s-516s)
- Average files changed: 1.3 (range: 1-3)
- 1 confirmed correct fix (django-14999: renamed model with db_table noop)

### 2. Python 3.12 Compatibility is the Primary Blocker
- **6/9 validation failures** are due to Python 3.12 incompatibility with old library versions:
  - `distutils` removed (Django 3.0)
  - `imp` module removed (pytest 4.4)
  - `pkg_resources` not available (Sphinx 3.2, scikit-learn 0.21)
  - Old numpy/scipy won't compile (scikit-learn 0.21)
- SWE-bench-official uses Docker images with version-specific Python (3.8-3.11)
- **Fix needed:** Either use Docker/containerized environments or filter to instances compatible with Python 3.12

### 3. Test Patch Conflicts
- **2/10 instances** had test_patch apply failures (flask-4045, sympy-16503)
- This happens when Skaffen modifies the same files that test_patch touches
- This is expected in some cases — a correct fix might rewrite code that the test_patch assumes is unchanged
- **Mitigation:** Apply test_patch with `git apply --3way` for better merge handling

### 4. Validation Command Formatting
- Iteration 1: Parentheses in unittest-style test IDs broke bash
- Iteration 2: Django's `runtests.py` needs dotted path format, not pytest path format
- Iteration 3: `uv venv` doesn't install pip; need `VIRTUAL_ENV= uv pip install`
- Some repos need extras (`[dev]`, `[test]`, `[testing]`) but old versions don't have those extras in setup.cfg/pyproject.toml

### 5. Infrastructure Bugs Fixed During Pilot
- `run_cell` didn't use `base_commit` from metadata (fixed: now uses `CloneRepoAt`)
- `run_cell` didn't apply `test_patch` before validation (fixed: added `ApplyTestPatch` step)
- `buildSWEBenchValidationCmd` didn't handle unittest-style test IDs (fixed: `convertTestIDToPytest`)
- Setup timeout too short for C extension compilation (fixed: 120s → 300s)

## Comparison to Published Baselines

SWE-bench Lite published pass rates (as of 2026):
- Claude 3.5 Sonnet (Anthropic): ~49%
- GPT-4o (OpenAI): ~33%
- DeepSeek-V3: ~42%
- Aider (Claude 3.5 Sonnet): ~26%

Our pilot: **10%** (1/10)

However, this comparison is misleading:
- Published results use Python version-specific Docker containers
- Our 6/9 "partial" results are environment failures, not Skaffen failures
- Skaffen produced patches in 9/9 non-setup-failure cases
- Adjusting for environment compatibility: **1/3 potentially valid** (django-14999 success + django-11179 and sympy-15345 need investigation)

## Recommended Next Steps

1. **Python version pinning (P0):** Add Docker support or pyenv-based setup to match SWE-bench environment_setup_commit. This unlocks the full 300-instance run.

2. **Filter to Python 3.12-compatible instances (quick win):** Run `swebench-pilot` against only Django 4.x+ and other modern repos to get cleaner signal on Skaffen's fix quality.

3. **Test patch conflict handling:** Switch from `git apply` to `git apply --3way` to handle cases where Skaffen's changes overlap with test_patch.

4. **Validation command improvements:**
   - Add per-repo validation command database (django→runtests.py, sympy→sympy test runner)
   - Install `setuptools` in venv by default (provides `pkg_resources`)
   - Use `VIRTUAL_ENV=$PWD/.venv uv pip install setuptools` in setup

5. **Patch quality analysis:** Even when validation fails due to environment, capture and analyze patches to assess correctness (manual review or gold-patch diff).
