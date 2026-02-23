import { describe, expect, it } from "bun:test";
import { getDefinitionByNameOrId, type RunConfig, validateRunConfig } from "../storage/run-config";

describe("Run Config Validation", () => {
	describe("validateRunConfig", () => {
		it("should accept valid config", () => {
			const validConfig = {
				org_id: "a37b44de-e4f8-4d09-956a-9c1148f3adf5",
				project_id: "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb",
				org_type: "github",
				definitions: {
					dev: {
						definition_id: "e2016e4e-0172-47b3-a4ea-a3ee1a592dba",
						chunk_environment_id: null,
						default_branch: "main",
					},
				},
			};

			expect(() => validateRunConfig(validConfig)).not.toThrow();
		});

		it("should reject config with missing org_id", () => {
			const invalidConfig = {
				project_id: "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb",
				definitions: {},
			};

			expect(() => validateRunConfig(invalidConfig)).toThrow(/org_id/);
		});

		it("should reject config with empty org_id", () => {
			const invalidConfig = {
				org_id: "",
				project_id: "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb",
				definitions: {},
			};

			expect(() => validateRunConfig(invalidConfig)).toThrow(/org_id/);
		});

		it("should reject config with missing project_id", () => {
			const invalidConfig = {
				org_id: "a37b44de-e4f8-4d09-956a-9c1148f3adf5",
				definitions: {},
			};

			expect(() => validateRunConfig(invalidConfig)).toThrow(/project_id/);
		});

		it("should reject config with invalid UUID in definition_id", () => {
			const invalidConfig = {
				org_id: "a37b44de-e4f8-4d09-956a-9c1148f3adf5",
				project_id: "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb",
				definitions: {
					dev: {
						definition_id: "not-a-uuid",
						chunk_environment_id: null,
					},
				},
			};

			expect(() => validateRunConfig(invalidConfig)).toThrow(/definition_id/);
		});

		it("should reject config with invalid UUID in chunk_environment_id", () => {
			const invalidConfig = {
				org_id: "a37b44de-e4f8-4d09-956a-9c1148f3adf5",
				project_id: "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb",
				definitions: {
					dev: {
						definition_id: "e2016e4e-0172-47b3-a4ea-a3ee1a592dba",
						chunk_environment_id: "invalid-uuid",
					},
				},
			};

			expect(() => validateRunConfig(invalidConfig)).toThrow(/chunk_environment_id/);
		});

		it("should accept config with optional fields", () => {
			const validConfig = {
				org_id: "a37b44de-e4f8-4d09-956a-9c1148f3adf5",
				project_id: "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb",
				definitions: {
					dev: {
						definition_id: "e2016e4e-0172-47b3-a4ea-a3ee1a592dba",
						description: "Development environment",
						chunk_environment_id: "b3c27e5f-1234-5678-9abc-def012345678",
						default_branch: "develop",
					},
				},
			};

			expect(() => validateRunConfig(validConfig)).not.toThrow();
		});

		it("should accept config with multiple definitions", () => {
			const validConfig = {
				org_id: "a37b44de-e4f8-4d09-956a-9c1148f3adf5",
				project_id: "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb",
				org_type: "circleci",
				definitions: {
					dev: {
						definition_id: "e2016e4e-0172-47b3-a4ea-a3ee1a592dba",
						chunk_environment_id: null,
					},
					prod: {
						definition_id: "f3127f5f-0283-48c4-b5fb-b4ff2b693ccb",
						chunk_environment_id: "b3c27e5f-1234-5678-9abc-def012345678",
					},
				},
			};

			expect(() => validateRunConfig(validConfig)).not.toThrow();
		});

		it("should default org_type to github when not provided", () => {
			const configWithoutOrgType = {
				org_id: "a37b44de-e4f8-4d09-956a-9c1148f3adf5",
				project_id: "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb",
				definitions: {},
			};

			const validated = validateRunConfig(configWithoutOrgType);
			expect(validated.org_type).toBe("github");
		});

		it("should reject config with invalid org_type", () => {
			const invalidConfig = {
				org_id: "a37b44de-e4f8-4d09-956a-9c1148f3adf5",
				project_id: "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb",
				org_type: "invalid",
				definitions: {},
			};

			expect(() => validateRunConfig(invalidConfig)).toThrow(/org_type/);
		});
	});

	describe("getDefinitionByNameOrId", () => {
		const mockConfig: RunConfig = {
			org_id: "a37b44de-e4f8-4d09-956a-9c1148f3adf5",
			project_id: "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb",
			org_type: "github",
			definitions: {
				dev: {
					definition_id: "e2016e4e-0172-47b3-a4ea-a3ee1a592dba",
					chunk_environment_id: "b3c27e5f-1234-5678-9abc-def012345678",
					default_branch: "develop",
				},
				prod: {
					definition_id: "f3127f5f-0283-48c4-b5fb-b4ff2b693ccb",
					chunk_environment_id: null,
					default_branch: "main",
				},
			},
		};

		it("should find definition by name", () => {
			const result = getDefinitionByNameOrId(mockConfig, "dev");

			expect(result.definitionId).toBe("e2016e4e-0172-47b3-a4ea-a3ee1a592dba");
			expect(result.envId).toBe("b3c27e5f-1234-5678-9abc-def012345678");
			expect(result.branch).toBe("develop");
		});

		it("should handle definition with null environment", () => {
			const result = getDefinitionByNameOrId(mockConfig, "prod");

			expect(result.definitionId).toBe("f3127f5f-0283-48c4-b5fb-b4ff2b693ccb");
			expect(result.envId).toBeNull();
			expect(result.branch).toBe("main");
		});

		it("should use UUID directly when name not found", () => {
			const uuid = "a1b2c3d4-5678-90ab-cdef-1234567890ab";
			const result = getDefinitionByNameOrId(mockConfig, uuid);

			expect(result.definitionId).toBe(uuid);
			expect(result.envId).toBeUndefined();
			expect(result.branch).toBe("main");
		});

		it("should default to 'main' branch when not specified", () => {
			const configWithoutBranch: RunConfig = {
				org_id: "a37b44de-e4f8-4d09-956a-9c1148f3adf5",
				project_id: "f4e4a365-da1d-408f-8f9c-0d4cc87d01cb",
				org_type: "github",
				definitions: {
					test: {
						definition_id: "e2016e4e-0172-47b3-a4ea-a3ee1a592dba",
						chunk_environment_id: null,
					},
				},
			};

			const result = getDefinitionByNameOrId(configWithoutBranch, "test");
			expect(result.branch).toBe("main");
		});

		it("should throw error for invalid UUID format", () => {
			expect(() => getDefinitionByNameOrId(mockConfig, "not-a-valid-uuid")).toThrow(
				/Unknown definition name/,
			);
		});

		it("should throw error for non-existent name", () => {
			expect(() => getDefinitionByNameOrId(mockConfig, "nonexistent")).toThrow(
				/Unknown definition name/,
			);
		});

		it("should accept UUID with uppercase letters", () => {
			const uuid = "A1B2C3D4-5678-90AB-CDEF-1234567890AB";
			const result = getDefinitionByNameOrId(mockConfig, uuid);

			expect(result.definitionId).toBe(uuid);
		});
	});
});
