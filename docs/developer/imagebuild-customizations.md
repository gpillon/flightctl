# ImageBuild Customizations Development Guide

This guide explains how to add new customization fields to the ImageBuild system. It covers the complete flow from API schema definition to UI implementation and Containerfile generation.

## Table of Contents

1. [Architecture Overview](#architecture-overview)
2. [Components Overview](#components-overview)
3. [Adding New Fields: Step-by-Step Guide](#adding-new-fields-step-by-step-guide)
4. [Examples](#examples)
5. [Testing](#testing)
6. [Best Practices](#best-practices)

## Architecture Overview

The ImageBuild system consists of three main layers:

```
┌─────────────────────────────────────────────────────────────┐
│                      Frontend (UI)                          │
│  - Form Components (React/TypeScript)                       │
│  - Type Definitions                                         │
│  - Validation & Serialization                               │
└─────────────────────┬───────────────────────────────────────┘
                      │ HTTP/JSON
                      │ POST /api/v1/imagebuilds
                      ▼
┌─────────────────────────────────────────────────────────────┐
│                    Backend API (Go)                         │
│  - OpenAPI Schema                                           │
│  - Generated Types (from OpenAPI)                           │
│  - HTTP Handlers                                            │
└─────────────────────┬───────────────────────────────────────┘
                      │
                      ▼
┌─────────────────────────────────────────────────────────────┐
│                 Containerfile Generator                     │
│  - Reads ImageBuildSpec                                     │
│  - Generates Dockerfile/Containerfile                       │
│  - Applies customizations in correct order                  │
└─────────────────────────────────────────────────────────────┘
                      │
                      ▼
┌─────────────────────────────────────────────────────────────┐
│              Container Build Process (bootc)                │
└─────────────────────────────────────────────────────────────┘
```

## Components Overview

### 1. OpenAPI Schema
**Location**: `api/v1alpha1/openapi.yaml`

Defines the API contract for ImageBuild resources. This is the single source of truth for the data structure.

### 2. Backend Components

#### Generated Types
**Location**: `api/v1alpha1/types.gen.go`

Auto-generated Go types from OpenAPI schema. **Never edit manually**.

#### Containerfile Generator
**Location**: `internal/imagebuilder/containerfile_generator.go`

Responsible for converting `ImageBuildSpec` into a valid Containerfile with all customizations applied.

### 3. Frontend Components

#### Type Definitions
- **Location**: `flightctl-ui/libs/ui-components/src/components/ImageBuild/CreateImageBuild/types.ts`
- Defines form values structure (`ImageBuildFormValues`)

#### API Types
- **Location**: `flightctl-ui/libs/ui-components/src/components/ImageBuild/useImageBuilds.ts`
- Defines the API resource structure (`ImageBuild` type)
- Used for API requests and responses

#### Form Components
- **Location**: `flightctl-ui/libs/ui-components/src/components/ImageBuild/CreateImageBuild/steps/CustomizationsStep.tsx`
- React components for form inputs

#### Utilities
- **Location**: `flightctl-ui/libs/ui-components/src/components/ImageBuild/CreateImageBuild/utils.ts`
- Contains:
  - `getInitialValues()`: Initial form state
  - `getInitialValuesFromImageBuild()`: Load existing ImageBuild into form
  - `getValidationSchema()`: Yup validation schema
  - `generateContainerfile()`: Preview Containerfile generation
  - `getImageBuildResource()`: Convert form values to API request

## Adding New Fields: Step-by-Step Guide

### Step 1: Update OpenAPI Schema

**File**: `api/v1alpha1/openapi.yaml`

Add your field to the `ImageBuildCustomizations` schema:

```yaml
ImageBuildCustomizations:
  type: object
  properties:
    # Existing fields...
    packages:
      type: array
      description: List of packages to install.
      items:
        type: string
    
    # NEW FIELD - Boolean example
    enableMyFeature:
      type: boolean
      description: Enable my custom feature.
    
    # NEW FIELD - String array example
    myCustomList:
      type: array
      description: List of custom items.
      items:
        type: string
    
    # NEW FIELD - Complex object array example
    myCustomObjects:
      type: array
      description: List of custom objects.
      items:
        $ref: '#/components/schemas/MyCustomObject'

# If using complex objects, define them separately
MyCustomObject:
  type: object
  properties:
    name:
      type: string
      description: Object name.
    value:
      type: string
      description: Object value.
    enabled:
      type: boolean
      description: Whether this object is enabled.
  required:
    - name
```

**Naming Conventions**:
- Use camelCase for field names in OpenAPI (e.g., `enableEpel`, `coprRepos`)
- This will be converted to PascalCase in Go (e.g., `EnableEpel`, `CoprRepos`)
- Keep camelCase in TypeScript/JavaScript

### Step 2: Regenerate Go Types

Run the code generator to create Go types from the updated OpenAPI schema:

```bash
cd /root/workdir/flightctl
make generate
```

This will update `api/v1alpha1/types.gen.go` with your new fields.

**Verify**: Check that your field appears in the `ImageBuildCustomizations` struct:

```go
type ImageBuildCustomizations struct {
    // Existing fields...
    Packages *[]string `json:"packages,omitempty"`
    
    // Your new field
    EnableMyFeature *bool `json:"enableMyFeature,omitempty"`
    MyCustomList *[]string `json:"myCustomList,omitempty"`
}
```

### Step 3: Update Frontend Types

#### 3.1 Update Form Values Type

**File**: `flightctl-ui/libs/ui-components/src/components/ImageBuild/CreateImageBuild/types.ts`

```typescript
export type ImageBuildFormValues = {
  name: string;
  baseImage: string;
  customizations: {
    packages: string[];
    
    // Add your new fields
    enableMyFeature?: boolean;
    myCustomList?: string[];
    myCustomObjects?: Array<{
      name: string;
      value: string;
      enabled?: boolean;
    }>;
    
    // Other existing fields...
  };
  // ... rest of the type
};
```

#### 3.2 Update API Type

**File**: `flightctl-ui/libs/ui-components/src/components/ImageBuild/useImageBuilds.ts`

```typescript
export type ImageBuild = {
  apiVersion: string;
  kind: string;
  metadata: { /* ... */ };
  spec: {
    baseImage: string;
    customizations?: {
      packages?: string[];
      
      // Add your new fields (same as form values)
      enableMyFeature?: boolean;
      myCustomList?: string[];
      myCustomObjects?: Array<{
        name: string;
        value: string;
        enabled?: boolean;
      }>;
      
      // Other existing fields...
    };
    // ... rest of the spec
  };
  // ... rest of the type
};
```

### Step 4: Update Utility Functions

**File**: `flightctl-ui/libs/ui-components/src/components/ImageBuild/CreateImageBuild/utils.ts`

#### 4.1 Update Initial Values

```typescript
export const getInitialValues = (): ImageBuildFormValues => ({
  name: '',
  baseImage: '',
  customizations: {
    packages: [],
    
    // Add default values for your fields
    enableMyFeature: false,
    myCustomList: [],
    myCustomObjects: [],
    
    // Other fields...
  },
  // ... rest
});
```

#### 4.2 Update Load from ImageBuild

```typescript
export const getInitialValuesFromImageBuild = (imageBuild: ImageBuild): ImageBuildFormValues => ({
  name: imageBuild.metadata.name || '',
  baseImage: imageBuild.spec.baseImage || '',
  customizations: {
    packages: imageBuild.spec.customizations?.packages || [],
    
    // Add your fields with fallback values
    enableMyFeature: imageBuild.spec.customizations?.enableMyFeature || false,
    myCustomList: imageBuild.spec.customizations?.myCustomList || [],
    myCustomObjects: imageBuild.spec.customizations?.myCustomObjects || [],
    
    // Other fields...
  },
  // ... rest
});
```

#### 4.3 Update Validation Schema (Optional)

```typescript
export const getValidationSchema = (t: TFunction) =>
  Yup.object({
    // ... existing validations
    customizations: Yup.object({
      // Add validation for your fields if needed
      myCustomList: Yup.array().of(Yup.string()),
      myCustomObjects: Yup.array().of(
        Yup.object({
          name: Yup.string().required(t('Name is required')),
          value: Yup.string().required(t('Value is required')),
          enabled: Yup.boolean(),
        }),
      ),
    }),
  });
```

#### 4.4 Update Preview Generation

```typescript
export const generateContainerfile = (values: ImageBuildFormValues): string => {
  const lines: string[] = [];
  
  lines.push(`FROM ${values.baseImage}`);
  lines.push('');
  
  // Add your custom logic for preview
  if (values.customizations.enableMyFeature) {
    lines.push('# Enable My Feature');
    lines.push('RUN setup-my-feature');
    lines.push('');
  }
  
  if (values.customizations.myCustomList.length > 0) {
    lines.push('# My Custom List');
    values.customizations.myCustomList.forEach((item) => {
      lines.push(`RUN process-item ${item}`);
    });
    lines.push('');
  }
  
  // ... rest of generation
  return lines.join('\n');
};
```

#### 4.5 Update API Resource Conversion

**IMPORTANT**: This is where fields are often forgotten!

```typescript
export const getImageBuildResource = (values: ImageBuildFormValues): ImageBuild => ({
  apiVersion: 'flightctl.io/v1alpha1',
  kind: 'ImageBuild',
  metadata: {
    name: values.name,
  },
  spec: {
    baseImage: values.baseImage,
    customizations: values.customizations.packages.length > 0 ||
      values.customizations.enableMyFeature ||  // Add to condition
      values.customizations.myCustomList.length > 0 ||  // Add to condition
      values.customizations.myCustomObjects.length > 0 ||  // Add to condition
      /* ... other conditions ... */
      ? {
          packages: values.customizations.packages.length > 0 
            ? values.customizations.packages 
            : undefined,
          
          // Add your fields to the object
          enableMyFeature: values.customizations.enableMyFeature || undefined,
          myCustomList: values.customizations.myCustomList.length > 0 
            ? values.customizations.myCustomList 
            : undefined,
          myCustomObjects: values.customizations.myCustomObjects.length > 0 
            ? values.customizations.myCustomObjects 
            : undefined,
          
          // Other fields...
        }
      : undefined,
    // ... rest of spec
  },
});
```

### Step 5: Update UI Components

**File**: `flightctl-ui/libs/ui-components/src/components/ImageBuild/CreateImageBuild/steps/CustomizationsStep.tsx`

Add form inputs for your new fields:

#### Example: Boolean Field (Checkbox)

```typescript
import { Checkbox } from '@patternfly/react-core';

// Inside the component
const { values, setFieldValue } = useFormikContext<ImageBuildFormValues>();

// In the JSX
<StackItem>
  <FormGroup fieldId="enableMyFeature">
    <Checkbox
      id="enableMyFeature"
      label={t('Enable My Feature')}
      description={t('This enables my custom feature')}
      isChecked={values.customizations.enableMyFeature || false}
      onChange={(_, checked) => setFieldValue('customizations.enableMyFeature', checked)}
    />
  </FormGroup>
</StackItem>
```

#### Example: String Array Field

```typescript
// Add handler functions
const addCustomItem = () => {
  setFieldValue('customizations.myCustomList', [
    ...values.customizations.myCustomList,
    ''
  ]);
};

const removeCustomItem = (index: number) => {
  const newItems = values.customizations.myCustomList.filter((_, i) => i !== index);
  setFieldValue('customizations.myCustomList', newItems);
};

const updateCustomItem = (index: number, value: string) => {
  const newItems = [...values.customizations.myCustomList];
  newItems[index] = value;
  setFieldValue('customizations.myCustomList', newItems);
};

// In the JSX
<StackItem>
  <Title headingLevel="h3" size="lg">
    {t('My Custom List')}
  </Title>
  <FormGroup
    label={t('Custom items')}
    fieldId="myCustomList"
  >
    {values.customizations.myCustomList.map((item, index) => (
      <div key={index} style={{ display: 'flex', gap: '8px', marginBottom: '8px' }}>
        <TextInput
          id={`custom-item-${index}`}
          value={item}
          onChange={(_, value) => updateCustomItem(index, value)}
          placeholder={t('Enter item')}
          style={{ flex: 1 }}
        />
        <Button
          variant="plain"
          icon={<TrashIcon />}
          onClick={() => removeCustomItem(index)}
          aria-label={t('Remove item')}
        />
      </div>
    ))}
    <Button variant="link" icon={<PlusCircleIcon />} onClick={addCustomItem}>
      {t('Add item')}
    </Button>
  </FormGroup>
</StackItem>
```

#### Example: Complex Object Array

```typescript
// Add handler functions
const addCustomObject = () => {
  setFieldValue('customizations.myCustomObjects', [
    ...values.customizations.myCustomObjects,
    { name: '', value: '', enabled: true }
  ]);
};

const removeCustomObject = (index: number) => {
  const newObjects = values.customizations.myCustomObjects.filter((_, i) => i !== index);
  setFieldValue('customizations.myCustomObjects', newObjects);
};

const updateCustomObject = (
  index: number, 
  field: 'name' | 'value' | 'enabled', 
  value: string | boolean
) => {
  const newObjects = [...values.customizations.myCustomObjects];
  newObjects[index] = { ...newObjects[index], [field]: value };
  setFieldValue('customizations.myCustomObjects', newObjects);
};

// In the JSX
<StackItem>
  <Title headingLevel="h3" size="lg">
    {t('My Custom Objects')}
  </Title>
  <FormGroup
    label={t('Custom objects')}
    fieldId="myCustomObjects"
  >
    {values.customizations.myCustomObjects.map((obj, index) => (
      <FormSection key={index} style={{ marginBottom: '16px', padding: '16px', border: '1px solid #ccc' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '8px' }}>
          <Title headingLevel="h4" size="md">
            {t('Object {{index}}', { index: index + 1 })}
          </Title>
          <Button
            variant="plain"
            icon={<TrashIcon />}
            onClick={() => removeCustomObject(index)}
            aria-label={t('Remove object')}
          />
        </div>
        <FormGroup label={t('Name')} fieldId={`obj-name-${index}`} isRequired>
          <TextInput
            id={`obj-name-${index}`}
            value={obj.name}
            onChange={(_, value) => updateCustomObject(index, 'name', value)}
            placeholder={t('Object name')}
          />
        </FormGroup>
        <FormGroup label={t('Value')} fieldId={`obj-value-${index}`}>
          <TextInput
            id={`obj-value-${index}`}
            value={obj.value}
            onChange={(_, value) => updateCustomObject(index, 'value', value)}
            placeholder={t('Object value')}
          />
        </FormGroup>
        <FormGroup fieldId={`obj-enabled-${index}`}>
          <Checkbox
            id={`obj-enabled-${index}`}
            label={t('Enabled')}
            isChecked={obj.enabled !== false}
            onChange={(_, checked) => updateCustomObject(index, 'enabled', checked)}
          />
        </FormGroup>
      </FormSection>
    ))}
    <Button variant="link" icon={<PlusCircleIcon />} onClick={addCustomObject}>
      {t('Add object')}
    </Button>
  </FormGroup>
</StackItem>
```

### Step 6: Update Containerfile Generator

**File**: `internal/imagebuilder/containerfile_generator.go`

Add logic to generate Containerfile commands from your new fields:

```go
// In the Generate() method, add your logic in the appropriate position
func (g *ContainerfileGenerator) Generate() (string, error) {
    var builder strings.Builder

    // Start with base image
    builder.WriteString(fmt.Sprintf("FROM %s\n\n", g.spec.BaseImage))

    // Add your custom logic
    if g.spec.Customizations != nil && 
       g.spec.Customizations.EnableMyFeature != nil && 
       *g.spec.Customizations.EnableMyFeature {
        builder.WriteString("# Enable My Feature\n")
        builder.WriteString("RUN setup-my-feature\n\n")
    }

    // For string arrays
    if g.spec.Customizations != nil && 
       g.spec.Customizations.MyCustomList != nil && 
       len(*g.spec.Customizations.MyCustomList) > 0 {
        builder.WriteString("# Process Custom List\n")
        builder.WriteString("RUN ")
        for i, item := range *g.spec.Customizations.MyCustomList {
            if i > 0 {
                builder.WriteString(" && \\\n    ")
            }
            builder.WriteString(fmt.Sprintf("process-item %s", item))
        }
        builder.WriteString("\n\n")
    }

    // For complex objects
    if g.spec.Customizations != nil && 
       g.spec.Customizations.MyCustomObjects != nil && 
       len(*g.spec.Customizations.MyCustomObjects) > 0 {
        builder.WriteString("# Setup Custom Objects\n")
        builder.WriteString("RUN ")
        first := true
        for _, obj := range *g.spec.Customizations.MyCustomObjects {
            if obj.Enabled != nil && *obj.Enabled {
                if !first {
                    builder.WriteString(" && \\\n    ")
                }
                builder.WriteString(fmt.Sprintf(
                    "setup-object --name=%s --value=%s", 
                    obj.Name, 
                    obj.Value,
                ))
                first = false
            }
        }
        builder.WriteString("\n\n")
    }

    // ... rest of generation
    return builder.String(), nil
}
```

**Important Notes**:
- Always check for `nil` pointers in Go (all OpenAPI fields are pointers)
- Always check array lengths before iterating
- Maintain proper order of operations (e.g., install packages before running scripts)
- Use `base64` encoding for multi-line content to avoid shell escaping issues

## Examples

### Example 1: Boolean Flag (enableEpel)

**OpenAPI**:
```yaml
enableEpel:
  type: boolean
  description: Enable EPEL repositories.
```

**Frontend Type** (`types.ts`):
```typescript
customizations: {
  enableEpel?: boolean;
}
```

**UI Component**:
```typescript
<Checkbox
  id="enableEpel"
  label={t('Enable EPEL repositories')}
  isChecked={values.customizations.enableEpel || false}
  onChange={(_, checked) => setFieldValue('customizations.enableEpel', checked)}
/>
```

**Containerfile Generator**:
```go
if g.spec.Customizations != nil && 
   g.spec.Customizations.EnableEpel != nil && 
   *g.spec.Customizations.EnableEpel {
    builder.WriteString("# Enable EPEL repositories\n")
    builder.WriteString("RUN dnf -y install epel-release epel-next-release\n\n")
}
```

### Example 2: String Array (coprRepos)

**OpenAPI**:
```yaml
coprRepos:
  type: array
  description: List of COPR repositories to enable.
  items:
    type: string
```

**Frontend Type**:
```typescript
customizations: {
  coprRepos: string[];
}
```

**UI Component**:
```typescript
{values.customizations.coprRepos.map((repo, index) => (
  <div key={index} style={{ display: 'flex', gap: '8px' }}>
    <TextInput
      value={repo}
      onChange={(_, value) => updateCoprRepo(index, value)}
      placeholder={t('user/repo')}
    />
    <Button onClick={() => removeCoprRepo(index)}>
      <TrashIcon />
    </Button>
  </div>
))}
<Button onClick={addCoprRepo}>
  <PlusCircleIcon /> {t('Add COPR repository')}
</Button>
```

**Containerfile Generator**:
```go
if g.spec.Customizations != nil && 
   g.spec.Customizations.CoprRepos != nil && 
   len(*g.spec.Customizations.CoprRepos) > 0 {
    builder.WriteString("# Enable COPR repositories\n")
    builder.WriteString("RUN ")
    for i, repo := range *g.spec.Customizations.CoprRepos {
        if i > 0 {
            builder.WriteString(" && \\\n    ")
        }
        builder.WriteString(fmt.Sprintf("dnf copr enable -y %s", repo))
    }
    builder.WriteString("\n\n")
}
```

### Example 3: Complex Object with File Permissions

**OpenAPI**:
```yaml
files:
  type: array
  items:
    $ref: '#/components/schemas/ImageBuildFile'

ImageBuildFile:
  type: object
  properties:
    path:
      type: string
    content:
      type: string
    mode:
      type: string
      description: File permissions (e.g., "0644")
    user:
      type: string
    group:
      type: string
  required:
    - path
    - content
```

**Frontend Type**:
```typescript
files: Array<{
  path: string;
  content: string;
  mode?: string;
  user?: string;
  group?: string;
}>;
```

**UI Component**:
```typescript
<FormSection>
  <FormGroup label={t('Path')} isRequired>
    <TextInput value={file.path} onChange={...} />
  </FormGroup>
  <FormGroup label={t('Content')}>
    <TextArea value={file.content} onChange={...} />
  </FormGroup>
  <FormGroup label={t('Mode (permissions)')}>
    <TextInput 
      value={file.mode || ''} 
      placeholder={t('0644')}
      onChange={...} 
    />
  </FormGroup>
  <FormGroup label={t('Owner user')}>
    <TextInput value={file.user || ''} onChange={...} />
  </FormGroup>
  <FormGroup label={t('Owner group')}>
    <TextInput value={file.group || ''} onChange={...} />
  </FormGroup>
</FormSection>
```

**Containerfile Generator**:
```go
func (g *ContainerfileGenerator) getFileCommands(file api.ImageBuildFile) ([]string, error) {
    var cmds []string

    // Write file content (base64 encoded)
    encodedContent := base64.StdEncoding.EncodeToString([]byte(file.Content))
    cmds = append(cmds, fmt.Sprintf("mkdir -p $(dirname %s)", file.Path))
    cmds = append(cmds, fmt.Sprintf("echo '%s' | base64 -d > %s", encodedContent, file.Path))

    // Set permissions if specified
    if file.Mode != nil && *file.Mode != "" {
        cmds = append(cmds, fmt.Sprintf("chmod %s %s", *file.Mode, file.Path))
    }

    // Set owner if specified
    if file.User != nil && *file.User != "" {
        owner := *file.User
        if file.Group != nil && *file.Group != "" {
            owner = fmt.Sprintf("%s:%s", *file.User, *file.Group)
        }
        cmds = append(cmds, fmt.Sprintf("chown %s %s", owner, file.Path))
    }

    return cmds, nil
}
```

## Testing

### 1. Backend Testing

Test the Containerfile generator:

```go
func TestEnableEpel(t *testing.T) {
    enableEpel := true
    spec := api.ImageBuildSpec{
        BaseImage: "registry.redhat.io/rhel9/rhel-bootc:latest",
        Customizations: &api.ImageBuildCustomizations{
            EnableEpel: &enableEpel,
        },
    }

    generator := NewContainerfileGenerator(spec, "")
    containerfile, err := generator.Generate()
    
    require.NoError(t, err)
    assert.Contains(t, containerfile, "dnf -y install epel-release epel-next-release")
}
```

### 2. Frontend Testing

Test the form:

1. **Manual Testing**:
   - Navigate to Create ImageBuild page
   - Fill in your new fields
   - Check the preview Containerfile
   - Submit the form
   - Verify the API request payload (use browser DevTools)

2. **Unit Testing** (if applicable):
```typescript
describe('getImageBuildResource', () => {
  it('includes enableEpel in customizations', () => {
    const values: ImageBuildFormValues = {
      // ... base values
      customizations: {
        enableEpel: true,
        // ... other fields
      },
    };

    const resource = getImageBuildResource(values);
    
    expect(resource.spec.customizations?.enableEpel).toBe(true);
  });
});
```

### 3. Integration Testing

1. Create an ImageBuild with your new fields via UI
2. Check the generated Containerfile in the ImageBuild details
3. Verify the built image contains your customizations
4. Test edge cases (empty values, null values, etc.)

## Best Practices

### 1. Naming Conventions

- **OpenAPI**: Use `camelCase` (e.g., `enableEpel`, `coprRepos`)
- **Go**: Generated as `PascalCase` (e.g., `EnableEpel`, `CoprRepos`)
- **TypeScript**: Keep `camelCase` (e.g., `enableEpel`, `coprRepos`)

### 2. Optional vs Required Fields

- Try to make fields optional unless absolutely required
- Use pointers in Go for optional fields (`*bool`, `*string`, `*[]string`)
- Use optional properties in TypeScript (`field?: type`)
- Always provide sensible defaults

### 3. Validation

- Add validation in OpenAPI schema when possible
- Add Yup validation in frontend for better UX
- Validate in backend if needed (business logic)

### 4. Error Handling

- Always check for `nil` pointers in Go
- Always check array lengths before iterating
- Provide meaningful error messages
- Handle edge cases gracefully

### 5. Documentation

- Add clear descriptions in OpenAPI schema
- Use descriptive labels and help text in UI
- Add inline comments in code for complex logic
- Update this guide when adding new patterns

### 6. Order of Operations

The order in which customizations are applied matters. Current order:

1. Create users
2. Enable EPEL (if selected)
3. Enable COPR repositories
4. Add custom files
5. Run custom scripts
6. Install additional packages
7. Add systemd units
8. Enable Podman (if selected)
9. Configure SSH keys for root
10. Install flightctl agent
11. Configure flightctl agent

Maintain this order or document changes clearly.

### 7. Security Considerations

- Never expose sensitive data in preview (TODO: REMOVE SENSITIVE DATA!)
- Use base64 encoding for multi-line content
- Validate file paths to prevent directory traversal
- Sanitize user input properly
- Be careful with shell command injection

### 8. Backward Compatibility

- New fields should be optional
- Provide defaults for new fields when loading old ImageBuilds
- Test with existing ImageBuilds to ensure they still work
- Document breaking changes clearly

## Common Pitfalls

### 1. Forgetting to Update `getImageBuildResource()`

**Problem**: New fields are not sent to the API.

**Solution**: Always add your fields to both the condition and the object in `getImageBuildResource()`.

### 2. Type Mismatches

**Problem**: TypeScript compilation errors about incompatible types.

**Solution**: Ensure all three type definitions are aligned:
- `ImageBuildFormValues` (types.ts)
- `ImageBuild` (useImageBuilds.ts)
- Field types must match exactly

### 3. Missing Nil Checks in Go

**Problem**: Panic when accessing optional fields.

**Solution**: Always check for nil before dereferencing:
```go
if field != nil && *field != "" {
    // Use *field
}
```

### 4. Array Bounds

**Problem**: Panic when accessing array elements.

**Solution**: Always check length before iterating:
```go
if myArray != nil && len(*myArray) > 0 {
    for _, item := range *myArray {
        // Process item
    }
}
```

### 5. Missing Initial Values

**Problem**: Form doesn't work correctly on edit.

**Solution**: Add your fields to `getInitialValuesFromImageBuild()`.

### 6. Preview Not Showing New Fields

**Problem**: New fields don't appear in Containerfile preview.

**Solution**: Update `generateContainerfile()` in `utils.ts`.

## Checklist

When adding a new field, use this checklist:

- [ ] Updated OpenAPI schema (`openapi.yaml`)
- [ ] Regenerated Go types (`make generate`)
- [ ] Updated `ImageBuildFormValues` type (`types.ts`)
- [ ] Updated `ImageBuild` API type (`useImageBuilds.ts`)
- [ ] Updated `getInitialValues()` (`utils.ts`)
- [ ] Updated `getInitialValuesFromImageBuild()` (`utils.ts`)
- [ ] Updated `getImageBuildResource()` - **condition** (`utils.ts`)
- [ ] Updated `getImageBuildResource()` - **object** (`utils.ts`)
- [ ] Updated `generateContainerfile()` for preview (`utils.ts`)
- [ ] Added validation schema if needed (`utils.ts`)
- [ ] Added UI components (`CustomizationsStep.tsx`)
- [ ] Updated Containerfile generator (`containerfile_generator.go`)
- [ ] Tested actual ImageBuild creation

## Questions?

If you have questions or need help:

1. Check existing similar fields for examples
2. Review this documentation
3. Check the Git history for recent field additions
4. Ask the team on Slack/GitHub

## Contributing

When you add new fields:

1. Follow the patterns established in this guide
2. Update this documentation if you introduce new patterns
3. Add tests for your new fields
4. Document any special considerations
5. Review the checklist :) 

