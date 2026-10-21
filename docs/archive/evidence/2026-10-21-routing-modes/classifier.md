Classifier Jev (jev-1.13.0).

| # | Expected | Classified (p) | Activity (p) | Match | cost | balanced | quality |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | micro / explore | micro (0.88) | ops (0.56) | activity | 6-luna/medium | 6-luna/high | 6.1-sol/medium |
| 2 | micro / ops | micro (0.87) | ops (1.00) | yes | 6-luna/medium | 6-luna/high | 6.1-sol/medium |
| 3 | micro / docs | micro (0.96) | docs (0.96) | yes | 6-luna/medium | 6-luna/medium | 6-luna/medium |
| 4 | low / code | micro (0.53) | code (1.00) | tier | 6-luna/medium | 6.1-sol/medium | 6.1-sol/medium |
| 5 | low / docs | micro (0.79) | docs (0.92) | tier | 6-luna/medium | 6-luna/high | 6-luna/xhigh |
| 6 | low / explore | micro (0.66) | explore (1.00) | tier | 6-luna/high | 6.1-sol/medium | 6.1-sol/medium |
| 7 | low / ops | micro (0.66) | code (1.00) | tier, activity | 6-luna/medium | 6-luna/high | 6.1-sol/medium |
| 8 | low / review | medium (0.51) | review (1.00) | tier | 6-luna/high | 6.1-sol/medium | 6.1-sol/medium |
| 9 | medium / code | medium (0.68) | code (1.00) | yes | 6-luna/high | 6.1-sol/medium | 6.1-sol/high |
| 10 | medium / debug | high (0.65) | debug (1.00) | tier | 6.1-sol/medium | 6.1-sol/high | 6.1-sol/xhigh |
| 11 | medium / review | high (0.85) | review (1.00) | tier | 6.1-sol/medium | 6.1-sol/high | 6.1-sol/xhigh |
| 12 | medium / plan | medium (0.85) | plan (1.00) | yes | 6-luna/high | 6.1-sol/medium | 6.1-sol/high |
| 13 | medium / ops | medium (0.49) | debug (1.00) | activity | 6.1-sol/medium | 6.1-sol/high | 6.1-sol/xhigh |
| 14 | medium / explore | high (0.66) | explore (1.00) | tier | 6.1-sol/medium | 6.1-sol/medium | 6.1-sol/xhigh |
| 15 | medium / docs | medium (0.59) | docs (1.00) | yes | 6-luna/high | 6.1-sol/medium | 6.1-sol/high |
| 16 | high / debug | high (1.00) | debug (0.97) | yes | 6.1-sol/medium | 6.1-sol/high | 6.1-sol/xhigh |
| 17 | high / plan | high (1.00) | plan (0.98) | yes | 6.1-sol/medium | 6.1-sol/high | 6.1-sol/xhigh |
| 18 | high / code | high (1.00) | code (1.00) | yes | 6.1-sol/medium | 6.1-sol/high | 6.1-sol/xhigh |
| 19 | high / review | high (1.00) | review (1.00) | yes | 6.1-sol/medium | 6.1-sol/high | 6.1-sol/xhigh |
| 20 | uncertain / uncertain | uncertain (1.00) | uncertain (1.00) | yes | 6.1-sol/medium | 6.1-sol/medium | 6.1-sol/medium |

10 of 20 prompts classified other than expected.

Estimated cost assumes one uncached turn per prompt of 50000 input and 2000 output tokens at list prices; reasoning tokens are not modeled.

| Mode | Est. cost | Models | Efforts |
| --- | --- | --- | --- |
| cost | $1.146 | map[6-luna:11 6.1-sol:9] | map[high:5 medium:15] |
| balanced | $1.830 | map[6-luna:5 6.1-sol:15] | map[high:11 medium:9] |
| quality | $2.172 | map[6-luna:2 6.1-sol:18] | map[high:3 medium:8 xhigh:9] |
