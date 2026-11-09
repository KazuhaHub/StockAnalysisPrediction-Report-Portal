// Derived from the Portal descriptor schema; edit the source contract instead.
export const DESCRIPTOR_SCHEMA_SHA256 = "776619608942d190cdda4e4926475bd413a8dbadf6513bc7ae66a10a79d9d3e9";
export interface Descriptor {
 format: "portal.app/v2";
 id: string;
 version: string;
 publisher: string;
 compatibility: Compatibility;
 presentation: Presentation;
 frontend: Artifact;
 backend?: Backend | null;
 capabilities?: Capabilities | null;
 data: DataCompatibility;
}
export interface Compatibility {
 platform_api: string;
 runner_protocol: string;
}
export interface Presentation {
 name_key: string;
 description_key: string;
 locales: ("en-US" | "zh-CN" | "zh-TW")[];
 routes: (Route)[];
 entry: string;
 display?: AppDisplay | null;
}
export interface Route {
 id: string;
 path: string;
 layout: "document" | "viewport";
 query?: (RouteQuery)[] | null;
}
export interface Artifact {
 artifact: string;
 sha256: string;
}
export interface Backend {
 kind: "oci";
 image?: string;
 images?: (BackendImage)[];
 platforms: ("linux/amd64" | "linux/arm64")[];
 protocol: "portal.backend/1";
 resources: Resources;
}
export interface BackendImage {
 platform: "linux/amd64" | "linux/arm64";
 manifest_digest: string;
 config_digest: string;
 layers: (ImageLayer)[];
}
export interface ImageLayer {
 diff_id: string;
 uncompressed_bytes: number;
}
export interface Resources {
 memory_mib: number;
 cpu_millis: number;
 pids: number;
}
export interface Capabilities {
 required?: ("reports.read" | "storage.files" | "tasks.schedule" | "events.subscribe" | "notifications.publish" | "network.http" | "network.stream" | "secrets.use" | "secrets.read" | "background.run" | "files.export")[] | null;
 optional?: ("reports.read" | "storage.files" | "tasks.schedule" | "events.subscribe" | "notifications.publish" | "network.http" | "network.stream" | "secrets.use" | "secrets.read" | "background.run" | "files.export")[] | null;
}
export interface DataCompatibility {
 schema: number;
 readable_schemas: (number)[];
 writes_on_startup?: false | null;
 migrations?: (DataMigration)[];
}
export interface DataMigration {
 from: number;
 to: number;
 entrypoint: (string)[];
 wall_clock_seconds?: number;
}
export interface AppDisplay {
 icon: "appstore" | "file-text" | "dashboard" | "calendar" | "bar-chart" | "cloud" | "tool" | "experiment";
 texts: (AppDisplayText)[];
}
export interface AppDisplayText {
 locale: "en-US" | "zh-CN" | "zh-TW";
 name: string;
 description: string;
}
export interface RouteQuery {
 name: string;
 type: "text" | "integer" | "boolean" | "enum";
 max_bytes?: number;
 min?: number;
 max?: number;
 values?: (string)[];
}
export const DESCRIPTOR_LIMITS = {
  "MaxDescriptorBytes": 131072,
  "MaxAppNameCharacters": 80,
  "MaxAppDescriptionCharacters": 320,
  "maxDescriptorDepth": 24,
  "maxRouteQueryFields": 16,
  "maxRouteQueryTextBytes": 256,
  "maxRouteQueryInteger": 1000000000,
  "maxRouteQueryValues": 16,
  "maxDescriptorRoutes": 32,
  "maxDescriptorReadableSchemas": 32,
  "maxDescriptorMigrations": 8,
  "maxMigrationEntrypointArgs": 16,
  "maxMigrationEntrypointBytes": 256,
  "minMigrationWallClockSeconds": 1,
  "maxMigrationWallClockSeconds": 600,
  "minDescriptorDataSchema": 1,
  "maxDescriptorDataSchema": 1000000,
  "maxDescriptorPathBytes": 256,
  "maxBackendImageBytes": 512,
  "maxBackendImagePlatforms": 2,
  "maxImageLayers": 32,
  "maxImageLayerUncompressedBytes": 1073741824,
  "maxDescriptorVersionBytes": 128,
  "minBackendMemoryMiB": 16,
  "maxBackendMemoryMiB": 4096,
  "minBackendCPUMillis": 25,
  "maxBackendCPUMillis": 4000,
  "minBackendPIDs": 16,
  "maxBackendPIDs": 1024
} as const;
export const DESCRIPTOR_CONSTANTS = {
  "descriptorFormat": "portal.app/v2",
  "descriptorBackendKind": "oci",
  "descriptorBackendProtocol": "portal.backend/1"
} as const;
export const DESCRIPTOR_PATTERNS = {
  "queryName": "^[a-z][a-z0-9_]{0,31}$",
  "queryEnum": "^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$",
  "slug": "^[a-z][a-z0-9-]{0,63}$",
  "key": "^[a-z][a-z0-9_]*$",
  "copyKey": "^[a-z][a-z0-9_.-]{0,95}$",
  "digest": "^[a-f0-9]{64}$",
  "version": "^(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\\.[0-9A-Za-z-]+)*)?(\\+[0-9A-Za-z-]+(\\.[0-9A-Za-z-]+)*)?$",
  "range": "^[0-9A-Za-z.*+<>=~^| -]{1,96}$",
  "image": "^[a-z0-9][a-z0-9.-]*(:[0-9]{1,5})?/[a-z0-9][a-z0-9._/-]*@sha256:[a-f0-9]{64}$",
  "path": "^[A-Za-z0-9._/-]+$"
} as const;
export const DESCRIPTOR_CAPABILITIES = [
  "reports.read",
  "storage.files",
  "tasks.schedule",
  "events.subscribe",
  "notifications.publish",
  "network.http",
  "network.stream",
  "secrets.use",
  "secrets.read",
  "background.run",
  "files.export"
] as const;
export const DESCRIPTOR_LOCALES = [
  "en-US",
  "zh-CN",
  "zh-TW"
] as const;
export const DESCRIPTOR_DISPLAY_ICONS = [
  "appstore",
  "file-text",
  "dashboard",
  "calendar",
  "bar-chart",
  "cloud",
  "tool",
  "experiment"
] as const;
export const DESCRIPTOR_PLATFORMS = [
  "linux/amd64",
  "linux/arm64"
] as const;
export const DESCRIPTOR_LAYOUTS = [
  "document",
  "viewport"
] as const;
export const DESCRIPTOR_FIELDS = {
  "Manifest": [
    "format",
    "id",
    "version",
    "publisher",
    "compatibility",
    "presentation",
    "frontend",
    "backend",
    "capabilities",
    "data"
  ],
  "Compatibility": [
    "platform_api",
    "runner_protocol"
  ],
  "Presentation": [
    "name_key",
    "description_key",
    "locales",
    "routes",
    "entry",
    "display"
  ],
  "Route": [
    "id",
    "path",
    "layout",
    "query"
  ],
  "Artifact": [
    "artifact",
    "sha256"
  ],
  "Backend": [
    "kind",
    "image",
    "images",
    "platforms",
    "protocol",
    "resources"
  ],
  "BackendImage": [
    "platform",
    "manifest_digest",
    "config_digest",
    "layers"
  ],
  "ImageLayer": [
    "diff_id",
    "uncompressed_bytes"
  ],
  "Resources": [
    "memory_mib",
    "cpu_millis",
    "pids"
  ],
  "Capabilities": [
    "required",
    "optional"
  ],
  "DataCompatibility": [
    "schema",
    "readable_schemas",
    "writes_on_startup",
    "migrations"
  ],
  "DataMigration": [
    "from",
    "to",
    "entrypoint",
    "wall_clock_seconds"
  ],
  "AppDisplay": [
    "icon",
    "texts"
  ],
  "AppDisplayText": [
    "locale",
    "name",
    "description"
  ],
  "RouteQuery": [
    "name",
    "type",
    "max_bytes",
    "min",
    "max",
    "values"
  ]
} as const;
