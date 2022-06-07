# Terragrunt Utils

Parse terragrunt file

## Example

```go
content, err := ioutil.ReadFile(terragruntFilename)
if err != nil {
	return
}

terragruntConfig, err := terragrunt.ParseConfig(content)
if err != nil {
	return
}
```
