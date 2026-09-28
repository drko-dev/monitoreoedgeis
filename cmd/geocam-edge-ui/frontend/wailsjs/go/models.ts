export namespace installer {
	
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

}
