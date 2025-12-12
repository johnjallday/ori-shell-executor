# ori-shell-executor Improvements

## Security Enhancements

1. **PowerShell support on Windows** - `cmd /C` is limited; PowerShell is more capable
2. **Environment variable filtering** - Block sensitive env vars from being passed to commands
3. **Output size limits** - Prevent memory issues from commands with huge output
4. **Command logging/audit trail** - Log executed commands for security review

## Functionality

5. **Async/background commands** - Run long commands without blocking
6. **Stdin support** - Allow piping input to commands
7. **Shell selection** - Let users choose shell (bash, zsh, fish, powershell)
8. **Working directory validation improvement** - Resolve symlinks to prevent bypass

## Usability

9. **Pre-defined command templates** - Safe shortcuts like "git status", "go build"
10. **Dry-run mode** - Show what would execute without running
11. **Interactive confirmation** - Require approval for certain patterns

## Code Quality

12. **Unit tests** - Test pattern matching, validation, Windows/Unix paths
13. **Better error messages** - More helpful when commands are blocked

## Recommended Starting Points

- **#12 (Unit tests)** - Important for reliability
- **#3 (Output size limits)** - Prevents runaway memory usage
- **#1 (PowerShell)** - Better Windows experience
