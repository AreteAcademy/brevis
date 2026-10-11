package sdk

// DescribeForTest exposes the pipeline's description to the external tests.
func DescribeForTest(p *Pipeline) map[string]any { return p.describe() }
