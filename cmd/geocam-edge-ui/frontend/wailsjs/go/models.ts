export namespace installer {

	export class ClaimRequest {
	    code: string;
	    device_name?: string;
	    saas_url?: string;

	    static createFrom(source: any = {}) {
	        return new ClaimRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.code = source["code"];
	        this.device_name = source["device_name"];
	        this.saas_url = source["saas_url"];
	    }
	}
	export class ClaimResult {
	    device_id: string;
	    organization_id: number;
	    device_kind: string;
	    status: string;
	    edge_id: string;

	    static createFrom(source: any = {}) {
	        return new ClaimResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.device_id = source["device_id"];
	        this.organization_id = source["organization_id"];
	        this.device_kind = source["device_kind"];
	        this.status = source["status"];
	        this.edge_id = source["edge_id"];
	    }
	}
	export class CurrentProcessingMode {
	    mode: string;
	    pipeline_enabled: boolean;
	    effective_profile: string;
	    source: string;

	    static createFrom(source: any = {}) {
	        return new CurrentProcessingMode(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	        this.pipeline_enabled = source["pipeline_enabled"];
	        this.effective_profile = source["effective_profile"];
	        this.source = source["source"];
	    }
	}
	export class InstallerState {
	    state: string;
	    reason_code: string;
	    safe_message: string;
	    recoverable: boolean;
	    next_allowed_actions: string[];
	    device_id?: string;

	    static createFrom(source: any = {}) {
	        return new InstallerState(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.state = source["state"];
	        this.reason_code = source["reason_code"];
	        this.safe_message = source["safe_message"];
	        this.recoverable = source["recoverable"];
	        this.next_allowed_actions = source["next_allowed_actions"];
	        this.device_id = source["device_id"];
	    }
	}
	export class ProcessingModeApplyResult {
	    requested_product_mode: string;
	    expected_processing_mode: string;
	    expected_effective_profile: string;
	    actual_processing_mode: string;
	    actual_effective_profile: string;
	    match: boolean;
	    status: string;
	    restart_required: boolean;
	    rolled_back: boolean;
	    safe_message: string;
	    warnings?: string[];

	    static createFrom(source: any = {}) {
	        return new ProcessingModeApplyResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.requested_product_mode = source["requested_product_mode"];
	        this.expected_processing_mode = source["expected_processing_mode"];
	        this.expected_effective_profile = source["expected_effective_profile"];
	        this.actual_processing_mode = source["actual_processing_mode"];
	        this.actual_effective_profile = source["actual_effective_profile"];
	        this.match = source["match"];
	        this.status = source["status"];
	        this.restart_required = source["restart_required"];
	        this.rolled_back = source["rolled_back"];
	        this.safe_message = source["safe_message"];
	        this.warnings = source["warnings"];
	    }
	}
	export class ProcessingModeOption {
	    mode: string;
	    display_name: string;
	    description: string;
	    local_compute: string;
	    network_dependency: string;
	    inference_location: string;
	    capability: string;
	    capability_reason?: string;
	    warnings?: string[];
	    blockers?: string[];

	    static createFrom(source: any = {}) {
	        return new ProcessingModeOption(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	        this.display_name = source["display_name"];
	        this.description = source["description"];
	        this.local_compute = source["local_compute"];
	        this.network_dependency = source["network_dependency"];
	        this.inference_location = source["inference_location"];
	        this.capability = source["capability"];
	        this.capability_reason = source["capability_reason"];
	        this.warnings = source["warnings"];
	        this.blockers = source["blockers"];
	    }
	}
	export class ProcessingModePlan {
	    requested_mode: string;
	    current_mode: string;
	    current_effective_profile: string;
	    target_effective_profile: string;
	    config_changes: Record<string, string>;
	    restart_required: boolean;
	    components_required?: string[];
	    warnings?: string[];
	    blockers?: string[];
	    rollback_available: boolean;

	    static createFrom(source: any = {}) {
	        return new ProcessingModePlan(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.requested_mode = source["requested_mode"];
	        this.current_mode = source["current_mode"];
	        this.current_effective_profile = source["current_effective_profile"];
	        this.target_effective_profile = source["target_effective_profile"];
	        this.config_changes = source["config_changes"];
	        this.restart_required = source["restart_required"];
	        this.components_required = source["components_required"];
	        this.warnings = source["warnings"];
	        this.blockers = source["blockers"];
	        this.rollback_available = source["rollback_available"];
	    }
	}
	export class ProcessingModeRequest {
	    mode: string;

	    static createFrom(source: any = {}) {
	        return new ProcessingModeRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	    }
	}
	export class SystemReport {
	    os: string;
	    arch: string;
	    edge_version: string;
	    commit: string;
	    build_date: string;
	    hostname: string;
	    privilege_level: string;
	    platform_supported: boolean;
	    data_dir: string;
	    service_installed: boolean;
	    daemon_running: boolean;
	    owns_instance_lock: boolean;
	    config_present: boolean;
	    enrolled: boolean;
	    edge_id?: string;
	    total_ram_mb: number;
	    free_ram_mb: number;
	    disk_path: string;
	    free_disk_gb: number;
	    ffmpeg_present: boolean;
	    ffprobe_path?: string;
	    has_gpu_support: boolean;
	    gpu_info?: string;

	    static createFrom(source: any = {}) {
	        return new SystemReport(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.os = source["os"];
	        this.arch = source["arch"];
	        this.edge_version = source["edge_version"];
	        this.commit = source["commit"];
	        this.build_date = source["build_date"];
	        this.hostname = source["hostname"];
	        this.privilege_level = source["privilege_level"];
	        this.platform_supported = source["platform_supported"];
	        this.data_dir = source["data_dir"];
	        this.service_installed = source["service_installed"];
	        this.daemon_running = source["daemon_running"];
	        this.owns_instance_lock = source["owns_instance_lock"];
	        this.config_present = source["config_present"];
	        this.enrolled = source["enrolled"];
	        this.edge_id = source["edge_id"];
	        this.total_ram_mb = source["total_ram_mb"];
	        this.free_ram_mb = source["free_ram_mb"];
	        this.disk_path = source["disk_path"];
	        this.free_disk_gb = source["free_disk_gb"];
	        this.ffmpeg_present = source["ffmpeg_present"];
	        this.ffprobe_path = source["ffprobe_path"];
	        this.has_gpu_support = source["has_gpu_support"];
	        this.gpu_info = source["gpu_info"];
	    }
	}

}
