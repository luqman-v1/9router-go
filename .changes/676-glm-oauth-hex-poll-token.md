### fix(glm): mint the ZCode OAuth poll token as hex

Starting the GLM OAuth login always failed with `ZCode OAuth init failed
(HTTP 400): invalid_flow`: the poll bearer was minted with the package's
mixed-case `randomString(64)`, but zcode.z.ai parses the bearer as a hex
flow identifier (the CLI and upstream send 32 random bytes hex-encoded,
and anything else is answered with `code 3004 "invalid_flow"`). The token
is now 32 crypto/rand bytes hex-encoded, and the test mock enforces the
same charset the live endpoint does so a non-hex token cannot pass again.
