package ciconfig

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestExtractImages(t *testing.T) {
	tests := []struct {
		name       string
		config     string
		wantImages []JobImage
		wantCommon string
	}{
		{
			name: "single image",
			config: `
version: 2.1
jobs:
  test:
    docker:
      - image: cimg/node:24.21.0
      - image: cimg/postgres:16.0
    steps:
      - run: npm test
workflows:
  main:
    jobs: [test]
`,
			wantImages: []JobImage{{Job: "test", Image: "cimg/node:24.21.0"}},
			wantCommon: "cimg/node:24.21.0",
		},
		{
			name: "named executor and mapping form",
			config: `
version: 2.1
executors:
  node:
    docker:
      - image: cimg/node:24.21.0
jobs:
  lint:
    executor: node
    steps:
      - run: npm run lint
  test:
    executor:
      name: node
    steps:
      - run: npm test
workflows:
  main:
    jobs: [lint, test]
`,
			wantImages: []JobImage{{"lint", "cimg/node:24.21.0"}, {"test", "cimg/node:24.21.0"}},
			wantCommon: "cimg/node:24.21.0",
		},
		{
			name: "mixed images",
			config: `
version: 2.1
jobs:
  lint:
    docker: [{image: cimg/node:22.0}]
    steps: [{run: npm run lint}]
  test:
    docker: [{image: cimg/node:24.0}]
    steps: [{run: npm test}]
workflows:
  main:
    jobs: [lint, test]
`,
			wantImages: []JobImage{{"lint", "cimg/node:22.0"}, {"test", "cimg/node:24.0"}},
		},
		{
			name: "machine executor has no image",
			config: `
version: 2.1
jobs:
  lint:
    docker: [{image: cimg/go:1.24}]
    steps: [{run: make lint}]
  e2e:
    machine: true
    steps: [{run: make e2e}]
workflows:
  main:
    jobs: [lint, e2e]
`,
			wantImages: []JobImage{{"lint", "cimg/go:1.24"}, {"e2e", ""}},
		},
		{
			name: "parameterised image is unreadable",
			config: `
version: 2.1
jobs:
  test:
    parameters:
      tag: {type: string, default: "1"}
    docker: [{image: "cimg/go:<< parameters.tag >>"}]
    steps: [{run: go test ./...}]
workflows:
  main:
    jobs: [test]
`,
			wantImages: []JobImage{{"test", ""}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := Extract(writeConfig(t, "config.yml", tt.config))
			assert.NilError(t, err)
			assert.DeepEqual(t, res.Images, tt.wantImages)
			assert.Equal(t, res.CommonImage(), tt.wantCommon)
		})
	}
}
