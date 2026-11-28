### Changed

- **Refactor only, no behaviour change:** the error-classification detectors were split out of their original file into `errorclassify_detect.go` as part of the oversized-file plan. The rules, their order and the classification results are unchanged. Refs #1516.
