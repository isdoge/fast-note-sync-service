package app

import (
	"bytes"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	ut "github.com/go-playground/universal-translator"
	"github.com/go-playground/validator/v10"
	"github.com/haierkeys/fast-note-sync-service/pkg/json"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
)

type ValidError struct {
	Key     string
	Message string
}

type ValidErrors []*ValidError

func (v *ValidError) Error() string {
	return v.Message
}

func (v *ValidError) Field() string {
	return v.Key
}

func (v *ValidError) Map() map[string]string {
	return map[string]string{v.Key: v.Message}
}

func (v ValidErrors) Error() string {
	return strings.Join(v.Errors(), ",")
}

func (v ValidErrors) Errors() []string {
	var errs []string
	for _, err := range v {
		errs = append(errs, err.Error())
	}

	return errs
}

func (v ValidErrors) ErrorsToString() string {
	var errs []string
	for _, err := range v {
		errs = append(errs, err.Error())
	}

	return strings.Join(errs, ",")
}

func (v ValidErrors) Maps() []map[string]string {
	var maps []map[string]string
	for _, err := range v {
		maps = append(maps, err.Map())
	}

	return maps
}

func (v ValidErrors) MapsToString() string {
	maps := v.Maps()
	re, _ := json.Marshal(maps)
	return string(re)
}

// bindableBodyContentType reports whether the request carries a body whose declared
// Content-Type the current binder will actually parse.
//
// 为什么需要它：当请求带了 body 但 Content-Type 不是可解析类型时（text/plain、
// text/html、或未声明），gin 的 form binder 会**静默忽略 body**、只从 URL query
// 绑定字段。对 /api/note 这类"字段在 body 里"的写接口，这会让 vault/path 从 query
// 拿到、content 落空，请求仍返回 code=1 Success 并把既有内容写没（静默数据丢失）。
// 这类请求一律拒绝（Invalid Params），而不是继续静默绑定。
func bindableBodyContentType(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return true
	}
	// 无 body：query-only 绑定行为不变，保持向后兼容。
	if c.Request.ContentLength == 0 {
		return true
	}
	switch c.ContentType() {
	case binding.MIMEJSON,
		binding.MIMEXML, binding.MIMEXML2,
		binding.MIMEPROTOBUF,
		binding.MIMEMSGPACK, binding.MIMEMSGPACK2,
		binding.MIMEYAML,
		binding.MIMETOML,
		binding.MIMEPOSTForm,
		binding.MIMEMultipartPOSTForm:
		return true
	}
	return false
}

// BindAndValid bind request parameters and perform validation, supporting multiple languages
// BindAndValid 绑定请求参数并进行验证，支持多语言
func BindAndValid(c *gin.Context, obj interface{}) (bool, ValidErrors) {
	var errs ValidErrors

	// Reject bodies the binder would silently drop, instead of binding from the
	// query string alone and persisting empty content.
	// 拒绝会被 binder 静默丢弃的 body，避免只从查询串绑定而把内容写成空值。
	if !bindableBodyContentType(c) {
		errs = append(errs, &ValidError{
			Key:     "Content-Type",
			Message: "unsupported Content-Type for request body, use application/json (#kvpac-suxu2)",
		})
		return false, errs
	}

	// Use global validator for validation
	// 使用全局验证器进行验证
	if err := c.ShouldBind(obj); err != nil {
		// If verification fails, check error type
		// 如果验证失败，检查错误类型
		if validationErrors, ok := err.(validator.ValidationErrors); ok {
			// Get translator
			// 获取翻译器
			v := c.Value("trans")
			trans := v.(ut.Translator)

			// Iterate through validation errors and translate them
			// 遍历验证错误并进行翻译
			for _, validationErr := range validationErrors {
				translatedMsg := validationErr.Translate(trans) // 翻译错误消息
				errs = append(errs, &ValidError{
					Key:     validationErr.Field(),
					Message: translatedMsg,
				})
			}
		}

		return false, errs // Return validation error // 返回验证错误
	}

	if verr := normalizeBoundPaths(obj); verr != nil {
		return false, ValidErrors{verr}
	}

	allowedVaults, _ := c.Get("vaults")
	vaults, _ := allowedVaults.(string)
	if verr := verifyBoundVault(vaults, obj); verr != nil {
		return false, ValidErrors{verr}
	}

	return true, nil // Binding and validation both succeeded, returns true // 绑定和验证都成功，返回 true
}

// verifyBoundVault checks the vault actually bound into the request object against the
// token's vault allowlist (#458). The auth middleware / WS interceptor read the vault from the
// raw request (query first, exact JSON key, JSON only), which can differ from what the binder
// fills in (JSON body, case-insensitive keys, protobuf payloads); checking the bound value closes
// that gap. A restricted token must name an allowed vault, so an empty vault is rejected too.
// Objects without a string field named Vault are not checked.
// verifyBoundVault 按令牌的笔记库白名单校验实际绑定到请求对象里的 vault（#458）。鉴权中间件 / WS 拦截器
// 从原始请求取 vault（先 query、JSON 精确键名、只认 JSON），与绑定器实际填入的值（JSON 请求体、键名大小写不敏感、
// protobuf 载荷）可能不同；校验绑定后的值即可堵住这一差异。受限令牌必须指明允许的库，因此空 vault 也拒绝。
// 没有名为 Vault 的字符串字段的对象不做校验。
func verifyBoundVault(allowedVaults string, obj any) *ValidError {
	if allowedVaults == "" {
		return nil
	}
	vault, ok := boundStringField(obj, "Vault")
	if !ok {
		return nil
	}
	if util.VerifyVaultAccess(allowedVaults, vault) {
		return nil
	}
	return &ValidError{Key: "vault", Message: "Vault access restricted: " + vault}
}

// boundStringField returns a string field from a possibly nested pointer to a struct.
// boundStringField 从可多级解引用的结构体中读取字符串字段。
func boundStringField(obj any, name string) (string, bool) {
	v := reflect.ValueOf(obj)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return "", false
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return "", false
	}
	field := v.FieldByName(name)
	if !field.IsValid() || field.Kind() != reflect.String {
		return "", false
	}
	return field.String(), true
}

// RequestParam extracts the specified parameter from the request without consuming or breaking the request body stream.
// RequestParam 从请求中提取指定参数（支持 Query、Form、JSON Body），且不消费或破坏请求体流。
func RequestParam(c *gin.Context, key string) string {
	if v := c.Query(key); v != "" {
		return v
	}
	if v := c.PostForm(key); v != "" {
		return v
	}
	if c.Request != nil && strings.Contains(c.ContentType(), "application/json") && c.Request.Body != nil {
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err == nil {
			c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
			var bodyObj map[string]interface{}
			if err := json.Unmarshal(bodyBytes, &bodyObj); err == nil {
				if val, ok := bodyObj[key]; ok {
					if strVal, ok := val.(string); ok {
						return strVal
					}
					switch valType := val.(type) {
					case float64:
						return strconv.FormatFloat(valType, 'f', -1, 64)
					case bool:
						return strconv.FormatBool(valType)
					default:
						return fmt.Sprintf("%v", val)
					}
				}
			}
		}
	}
	return ""
}
