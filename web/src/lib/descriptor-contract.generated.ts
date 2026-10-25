// Derived from the Portal descriptor schema; edit the source contract instead.
export const DESCRIPTOR_SCHEMA_SHA256 = "faed84e86443a6a622d2b02507b6ebd6f57bbc14bfd43a01289ae80bf27ddab2";
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
}
export interface Route {
 id: string;
 path: string;
 layout: "document" | "viewport";
}
export interface Artifact {
 artifact: string;
 sha256: string;
}
export interface Backend {
 kind: "oci";
 image: string;
 platforms: ("linux/amd64" | "linux/arm64")[];
 protocol: "portal.backend/1";
 resources: Resources;
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
}
export const DESCRIPTOR_LIMITS = {
  "MaxDescriptorBytes": 131072,
  "maxDescriptorDepth": 24,
  "maxDescriptorRoutes": 32,
  "maxDescriptorReadableSchemas": 32,
  "minDescriptorDataSchema": 1,
  "maxDescriptorDataSchema": 1000000,
  "maxDescriptorPathBytes": 256,
  "maxBackendImageBytes": 512,
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
    "entry"
  ],
  "Route": [
    "id",
    "path",
    "layout"
  ],
  "Artifact": [
    "artifact",
    "sha256"
  ],
  "Backend": [
    "kind",
    "image",
    "platforms",
    "protocol",
    "resources"
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
    "writes_on_startup"
  ]
} as const;
