### fix(kiro): send shared IDE identity on social token refresh

Send Kiro IDE 1.4.154 identity headers (`User-Agent` and `x-amz-user-agent`) on
social token refresh via the shared `KiroServiceUserAgent` helper instead of the
retired `kiro-cli/1.0.0` User-Agent. Closes #1383.

Upstream parity: decolua/9router#1da78e2bf.
