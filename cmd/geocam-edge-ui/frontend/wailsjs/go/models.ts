export namespace installer {

	export class CameraOnboardingApplyResult {
	    status: string;
	    operation_id?: number;
	    candidate_key: string;
	    safe_message: string;
	    sync_observed: boolean;

	    static createFrom(source: any = {}) {
	        return new CameraOnboardingApplyResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = source["status"];
	        this.operation_id = source["operation_id"];
	        this.candidate_key = source["candidate_key"];
	        this.safe_message = source["safe_message"];
	        this.sync_observed = source["sync_observed"];
	    }
	}
	export class StreamProfile {
	    codec?: string;
	    width?: number;
	    height?: number;
	    fps?: number;

	    static createFrom(source: any = {}) {
	        return new StreamProfile(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.codec = source["codec"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.fps = source["fps"];
	    }
	}
	export class CameraOnboardingPlan {
	    candidate_key: string;
	    camera_name: string;
	    profile?: StreamProfile;
	    processing_mode: string;
	    credentials_valid: boolean;
	    blockers?: string[];
	    warnings?: string[];

	    static createFrom(source: any = {}) {
	        return new CameraOnboardingPlan(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.candidate_key = source["candidate_key"];
	        this.camera_name = source["camera_name"];
	        this.profile = this.convertValues(source["profile"], StreamProfile);
	        this.processing_mode = source["processing_mode"];
	        this.credentials_valid = source["credentials_valid"];
	        this.blockers = source["blockers"];
	        this.warnings = source["warnings"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CameraOnboardingRequest {
	    candidate_key: string;
	    camera_name: string;
	    manufacturer?: string;
	    model?: string;
	    username: string;
	    password: string;

	    static createFrom(source: any = {}) {
	        return new CameraOnboardingRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.candidate_key = source["candidate_key"];
	        this.camera_name = source["camera_name"];
	        this.manufacturer = source["manufacturer"];
	        this.model = source["model"];
	        this.username = source["username"];
	        this.password = source["password"];
	    }
	}
	export class CameraValidationResult {
	    candidate_key: string;
	    multi_source: boolean;
	    onvif_status: string;
	    onvif_reason?: string;
	    rtsp_status: string;
	    rtsp_reason?: string;
	    profile?: StreamProfile;
	    passed: boolean;

	    static createFrom(source: any = {}) {
	        return new CameraValidationResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.candidate_key = source["candidate_key"];
	        this.multi_source = source["multi_source"];
	        this.onvif_status = source["onvif_status"];
	        this.onvif_reason = source["onvif_reason"];
	        this.rtsp_status = source["rtsp_status"];
	        this.rtsp_reason = source["rtsp_reason"];
	        this.profile = this.convertValues(source["profile"], StreamProfile);
	        this.passed = source["passed"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
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
	export class OnboardingCandidate {
	    candidate_key: string;
	    host: string;
	    manufacturer?: string;
	    model?: string;
	    onvif_available: boolean;
	    auth_required: boolean;
	    multi_source: boolean;

	    static createFrom(source: any = {}) {
	        return new OnboardingCandidate(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.candidate_key = source["candidate_key"];
	        this.host = source["host"];
	        this.manufacturer = source["manufacturer"];
	        this.model = source["model"];
	        this.onvif_available = source["onvif_available"];
	        this.auth_required = source["auth_required"];
	        this.multi_source = source["multi_source"];
	    }
	}
	export class DiscoverCamerasResult {
	    candidates: OnboardingCandidate[];
	    duration_ms: number;

	    static createFrom(source: any = {}) {
	        return new DiscoverCamerasResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.candidates = this.convertValues(source["candidates"], OnboardingCandidate);
	        this.duration_ms = source["duration_ms"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
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
	export class TestCameraCredentialsRequest {
	    candidate_key: string;
	    username: string;
	    password: string;

	    static createFrom(source: any = {}) {
	        return new TestCameraCredentialsRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.candidate_key = source["candidate_key"];
	        this.username = source["username"];
	        this.password = source["password"];
	    }
	}

}
