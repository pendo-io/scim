package scim

import (
	"fmt"
	f "github.com/elimity-com/scim/internal/filter"
	"github.com/elimity-com/scim/logging"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/elimity-com/scim/errors"
	"github.com/elimity-com/scim/schema"
)

const (
	defaultStartIndex = 1
	fallbackCount     = 100
)

// getFilter returns a validated filter if present in the url query, nil otherwise.
func getFilter(r *http.Request, s schema.Schema, extensions ...schema.Schema) (*f.Validator, error) {
	filter := strings.TrimSpace(r.URL.Query().Get("filter"))
	if filter == "" {
		return nil, nil // No filter present.
	}

	validator, err := f.NewValidator(filter, s, extensions...)
	if err != nil {
		return nil, err
	}
	if err := validator.Validate(); err != nil {
		return nil, err
	}
	return &validator, nil
}

func getIntQueryParam(r *http.Request, key string, def int) (int, error) {
	strVal := r.URL.Query().Get(key)

	if strVal == "" {
		return def, nil
	}

	if intVal, err := strconv.Atoi(strVal); err == nil {
		return intVal, nil
	}

	return 0, fmt.Errorf("invalid query parameter, \"%s\" must be an integer", key)
}

func parseIdentifier(path, endpoint string) (string, error) {
	return url.PathUnescape(strings.TrimPrefix(path, endpoint+"/"))
}

// NewServer instantiates a new Server with all the required attributes.
func NewServer(config ServiceProviderConfig, resourceTypes []ResourceType, log logging.Logger) Server {
	return Server{Config: config, ResourceTypes: resourceTypes, Log: log}
}

// Server represents a SCIM server which implements the HTTP-based SCIM protocol that makes managing identities in multi-
// domain scenarios easier to support via a standardized service.
type Server struct {
	Config        ServiceProviderConfig
	ResourceTypes []ResourceType
	Log           logging.Logger
}

// EnsureLogger instantiates a Logger interface if one was not set up
func (s Server) EnsureLogger() {
	if s.Log == nil {
		panic("logger not instantiated")
	}
}

// ServeHTTP dispatches the request to the handler whose pattern most closely matches the request URL.
func (s Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.EnsureLogger()
	w.Header().Set("Content-Type", "application/scim+json")

	path := strings.TrimPrefix(r.URL.Path, "/v2")

	switch {
	case path == "/Me":
		s.ErrorHandler(w, r, &errors.ScimError{
			Status: http.StatusNotImplemented,
		})
		return
	case path == "/Schemas" && r.Method == http.MethodGet:
		s.SchemasHandler(w, r)
		return
	case strings.HasPrefix(path, "/Schemas/") && r.Method == http.MethodGet:
		s.SchemaHandler(w, r, strings.TrimPrefix(path, "/Schemas/"))
		return
	case path == "/ResourceTypes" && r.Method == http.MethodGet:
		s.ResourceTypesHandler(w, r)
		return
	case strings.HasPrefix(path, "/ResourceTypes/") && r.Method == http.MethodGet:
		s.ResourceTypeHandler(w, r, strings.TrimPrefix(path, "/ResourceTypes/"))
		return
	case path == "/ServiceProviderConfig":
		s.ServiceProviderConfigHandler(w, r)
		return
	}

	for _, resourceType := range s.ResourceTypes {
		if path == resourceType.Endpoint {
			switch r.Method {
			case http.MethodPost:
				s.ResourcePostHandler(w, r, resourceType)
				return
			case http.MethodGet:
				s.ResourcesGetHandler(w, r, resourceType)
				return
			}
		}

		if strings.HasPrefix(path, resourceType.Endpoint+"/") {
			id, err := parseIdentifier(path, resourceType.Endpoint)
			if err != nil {
				break
			}

			switch r.Method {
			case http.MethodGet:
				s.ResourceGetHandler(w, r, id, resourceType)
				return
			case http.MethodPut:
				s.ResourcePutHandler(w, r, id, resourceType)
				return
			case http.MethodPatch:
				s.ResourcePatchHandler(w, r, id, resourceType)
				return
			case http.MethodDelete:
				s.ResourceDeleteHandler(w, r, id, resourceType)
				return
			}
		}
	}

	s.ErrorHandler(w, r, &errors.ScimError{
		Detail: "Specified endpoint does not exist.",
		Status: http.StatusNotFound,
	})
}

// getSchema extracts the schemas from the resources types defined in the server with given id.
func (s Server) getSchema(id string) schema.Schema {
	for _, resourceType := range s.ResourceTypes {
		if resourceType.Schema.ID == id {
			return resourceType.Schema
		}
		for _, extension := range resourceType.SchemaExtensions {
			if extension.Schema.ID == id {
				return extension.Schema
			}
		}
	}
	return schema.Schema{}
}

// getSchemas extracts all the schemas from the resources types defined in the server. Duplicate IDs will be ignored.
func (s Server) getSchemas() []schema.Schema {
	ids := make([]string, 0)
	schemas := make([]schema.Schema, 0)
	for _, resourceType := range s.ResourceTypes {
		if !contains(ids, resourceType.Schema.ID) {
			schemas = append(schemas, resourceType.Schema)
		}
		ids = append(ids, resourceType.Schema.ID)
		for _, extension := range resourceType.SchemaExtensions {
			if !contains(ids, extension.Schema.ID) {
				schemas = append(schemas, extension.Schema)
			}
			ids = append(ids, extension.Schema.ID)
		}
	}
	return schemas
}

func (s Server) parseRequestParams(r *http.Request, refSchema schema.Schema, refExtensions ...schema.Schema) (ListRequestParams, *errors.ScimError) {
	invalidParams := make([]string, 0)

	defaultCount := s.Config.getItemsPerPage()
	count, countErr := getIntQueryParam(r, "count", defaultCount)
	if countErr != nil {
		invalidParams = append(invalidParams, "count")
	}
	if count > defaultCount {
		// Ensure the count isn't more then the allowable max.
		count = defaultCount
	}
	if count < 0 {
		// A negative value shall be interpreted as 0.
		count = 0
	}

	startIndex, indexErr := getIntQueryParam(r, "startIndex", defaultStartIndex)
	if indexErr != nil {
		invalidParams = append(invalidParams, "startIndex")
	}
	if startIndex < 1 {
		startIndex = defaultStartIndex
	}

	if len(invalidParams) > 0 {
		scimErr := errors.ScimErrorBadParams(invalidParams)
		return ListRequestParams{}, &scimErr
	}

	reqFilter, err := getFilter(r, refSchema, refExtensions...)
	if err != nil {
		return ListRequestParams{}, &errors.ScimErrorInvalidFilter
	}

	return ListRequestParams{
		Count:           count,
		FilterValidator: reqFilter,
		StartIndex:      startIndex,
	}, nil
}
