// url format: http://origin/some/path/status?id=990b38ff-17f1-4ea8-98a1-31bb2cad8b9b

/*
*  This page polls the status API endpoint (every pollingIntervalMilliseconds + jitter) and builds a table of
*  information and a tree visualization of the environment being built/upgraded.
*/

/* See status.html for API endpoint definitions */

const pollingIntervalMilliseconds = 750;

let done = false;
let failures = 0;
let updateInterval = setInterval(update, pollingIntervalMilliseconds);
let env_name = "";
let active_pod_name = "";
let active_container = "";
let pod_log_lines = 100;

// https://stackoverflow.com/questions/21294302/converting-milliseconds-to-minutes-and-seconds-with-javascript
function millisToMinutesAndSeconds(millis) {
    var minutes = Math.floor(millis / 60000);
    var seconds = ((millis % 60000) / 1000).toFixed(0);
    return (seconds == 60 ? (minutes+1) + ":00" : minutes + ":" + (seconds < 10 ? "0" : "") + seconds);
}

function updateRefmap(refmap) {
    let header = document.getElementById("refmap-table-header");
    for (const [repo, ref] of Object.entries(refmap)) {
        if (document.getElementById(`refmap-table-row-${repo}`) === null) {
            let repolink = `https://github.com/${repo}`;
            let reflink = `https://github.com/${repo}/tree/${ref}`;
            let tr = document.createElement("tr");
            tr.id = `refmap-table-row-${repo}`;
            let tdrepo = document.createElement("td");
            tdrepo.innerHTML = `<a href="${repolink}">${repo}</a>`;
            let tdref = document.createElement("td");
            tdref.innerHTML = `<a href="${reflink}">${ref}</a>`;
            tr.appendChild(tdrepo);
            tr.appendChild(tdref);
            header.parentNode.insertBefore(tr, header.nextSibling);
        }
    }
}

function updateNSCopyBtn(k8s_ns) {
    if (k8s_ns === "") {
        document.getElementById("k8s-ns").innerHTML = "n/a";
        return;
    }
    if (document.getElementById("ns-copy-btn") === null) {
        const copybtn = `<span id="ns-copy-btn" style="cursor: pointer" data-toggle="tooltip" title="Copied!" data-trigger="click"><img class="nscopybtn" height="18" width="18" src="https://cdnjs.cloudflare.com/ajax/libs/octicons/8.5.0/svg/clippy.svg" alt="Copy to clipboard"></span>`;
        document.getElementById("k8s-ns").innerHTML = `${k8s_ns} ${copybtn}`;
        // have to use JQuery to manage the copy button tooltip
        $("#ns-copy-btn").tooltip();
        // copy namespace to clipboard button
        document.getElementById("ns-copy-btn").onclick = function(event) {
            event.preventDefault();
            navigator.clipboard.writeText(k8s_ns);
        };
        $("#ns-copy-btn").on('shown.bs.tooltip', function() {
            setTimeout(function() { $("#ns-copy-btn").tooltip('hide'); }, 500);
        });
    }
}

function updateBreadcrumb(cfg) {
    document.getElementById("bc-eventid").innerHTML = `Event: ${event_id}`;
    if (cfg.env_name !== "") {
        document.getElementById("bc-envname").innerHTML = `<a href="${apiBaseURL}/ui/env/${cfg.env_name}">${cfg.env_name}</a>`;
    }
}

function updateConfig(cfg) {
    let sicon = document.getElementById("status-icon");
    let slinkbtnclass = "";

    switch (cfg.status) {
        case "pending":
            slinkbtnclass = "btn-primary";
            sicon.innerHTML = "\uf28b";
            break;
        case "done":
            slinkbtnclass = "btn-success";
            sicon.innerHTML = "\uf058";
            break;
        case "failed":
            slinkbtnclass = "btn-danger";
            sicon.innerHTML = "\uf071";
            break;
        case "cancelled":
            slinkbtnclass = "btn-outline-light";
            sicon.innerHTML = "\uf05e";
        default:
            slinkbtnclass = "btn-warning";
            sicon.innerHTML = "";
    }
    document.getElementById("status-link-btn").className = `btn btn-sm ${slinkbtnclass}`;
    document.getElementById("status-link-btn").href = cfg.rendered_status.link_target_url;
    document.getElementById("status-link-title").innerHTML = `${cfg.rendered_status.description}`;
    const prurl = `https://github.com/${cfg.triggering_repo}/pull/${cfg.pull_request}`;
    document.getElementById("trepo-pr-link").text = prurl;
    document.getElementById("trepo-pr-link").href = prurl;
    const userurl = `https://github.com/${cfg.github_user}`;
    document.getElementById("trepo-user-link").text = userurl;
    document.getElementById("trepo-user-link").href = userurl;
    document.getElementById("env-name").innerHTML = `<b>${cfg.env_name}</b>`;
    updateNSCopyBtn(cfg.k8s_ns);
    document.getElementById("trepo-branch").innerHTML = cfg.branch;
    const revlink = `https://github.com/${cfg.triggering_repo}/commit/${cfg.revision}`;
    document.getElementById("trepo-revision").innerHTML = `<a href="${revlink}">${cfg.revision}</a>`;
    document.getElementById("event-type").innerHTML = cfg.type;
    if (cfg.status === 'failed') {
        document.getElementById("event-status").innerHTML = `<span class="badge badge-danger acyl-status pl-2 pr-0 py-0" style="font-size: medium">${cfg.status} <a class="fas btn btn-sm btn-danger" title="View Chart Error" href="${apiBaseURL}/ui/event/status/failure_report?id=${event_id}" role="button">&#xf35d</a></span>`;
    } else {
        document.getElementById("event-status").innerHTML = cfg.status;
    }
    document.getElementById("config-processing-duration").innerHTML = cfg.processing_time;
    document.getElementById("event-started-time").innerHTML = cfg.started;

    let start = new Date(cfg.started).getTime();
    let end = new Date().getTime();

    if (cfg.completed !== null) {
        done = true;
        end = new Date(cfg.completed).getTime();
        document.getElementById("event-completed-time").innerHTML = cfg.completed;
    }

    document.getElementById("event-elapsed").innerHTML = millisToMinutesAndSeconds(end - start);
    updateRefmap(cfg.ref_map);
    updateBreadcrumb(cfg);
}

function treeDimensions(levels) {
    let viewwidth = Math.max(document.documentElement.clientWidth, window.innerWidth || 0);

    let margin = {top: 50, right: 0, bottom: 0, left: 0},
        width = viewwidth - margin.right - margin.left,
        height = (100*levels) - margin.top - margin.bottom;

    return {
        width,
        height
    }
}

// stratify processes the flat object of nodes into a nested structure for D3
function stratify(treedata) {
    let out = [];
    const names = Object.keys(treedata);
    for (const name of names) {
        let obj = treedata[name];
        obj["name"] = name;
        out.push(obj);
    }
    return d3.stratify().id(function(d) { return d.name; }).parentId(function(d) { return d.parent; })(out);
}

// updateTree updates the existing D3 tree with the updated nodes
function updateTree(treedata) {

    let root = stratify(treedata);

    let tree = d3.layout.tree()
        .nodeSize([4,4])
        .separation(function(a,b) { return a.parent == b.parent ? 25 : 20 });

    let diagonal = d3.svg.diagonal()
        .projection(function (d) {
            return [d.x, d.y];
        });

    let nodes = tree.nodes(root).reverse(),
        links = tree.links(nodes);

    let maxdepth = 0;
    nodes.forEach(function(d) {
        if (d.depth > maxdepth) {
            maxdepth = d.depth;
        }
    });
    maxdepth++; // zero-indexed to one-indexed

    let svg;
    if (document.getElementById("envtree-svg") === null) {
        const {
            width,
            height,
        } = treeDimensions(maxdepth);

        svg = d3.select("#envtree").append("svg")
            .attr("id", "envtree-svg")
            .attr("width", width)
            .attr("height", height)
            .append("g")
            .style("transform", "translate(50%, 12%)");
    } else {
        svg = d3.select("#envtree-svg");
    }

    // Normalize for fixed-depth.
    nodes.forEach(function(d) {
        // Root node should be vertically offset to allow margin between the container and root node
        if (d.depth == 0) {
            d.y = 1;
        } else {
            // fixed depth of 90 pixels per tree level
            d.y = d.depth * 90;
        }
        if (document.getElementById(`tooltip-${d.id}`) === null) {
            let nttd = d3.select("body").append("div")
                .attr("class", "container")
                .append("div")
                .attr("id", `tooltip-${d.id}`)
                .attr("class", "tree-tooltip btn-group-vertical bg-dark text-light")
                .style("display", "none");

            nttd.on("mouseleave", function(d) {
                closeNodeTooltip(`tooltip-${d.id}`);
            });

            nttd.append("button")
                .attr("type", "button")
                .attr("class", "close text-light pb-2")
                .attr("aria-label", "Close")
                .attr("onclick", `closeNodeTooltip("tooltip-${d.id}");`)
                .html(`<span aria-hidden="true">&times;</span></button>`);

            nttd.append("h6")
                .attr("class", "pt-1")
                .attr("id", `tooltip-${d.id}-name`);

            nttd.append("button")
                .attr("type", "button")
                .attr("class", "tt-helm-btn btn btn-info btn-sm")
                .style("display", "none")
                .style("pointer-events", "none")
                .html(`<img src="https://helm.sh/img/helm.svg" height="16" width="16"> Chart Dependency`);

            nttd.append("button")
                .attr("type", "button")
                .style("display", "none")
                .style("pointer-events", "none")
                .attr("class", "tt-repo-btn btn btn-light btn-sm");

            nttd.append("button")
                .attr("id", `image-build-btn-${d.id}`)
                .attr("type", "button")
                .attr("data-toggle", "modal")
                .attr("data-target", `#image-modal-${d.id}`)
                .style("pointer-events", "none")
                .style("display", "none")
                .attr("class", "tt-image-btn btn btn-primary btn-sm");

            nttd.append("button")
                .attr("type", "button")
                .style("display", "none")
                .style("pointer-events", "none")
                .attr("class", "tt-chart-btn btn btn-secondary btn-sm")
                .html("Chart: Waiting");

            // image build details modal
            let mcnt = d3.select("body")
                .append("div")
                .attr("class", "modal")
                .attr("tabindex", "-1")
                .attr("role", "dialog")
                .attr("id", `image-modal-${d.id}`)
                .attr("aria-labelledby", `image-modal-${d.id}`)
                .attr("aria-hidden", "true")
                .append("div")
                .attr("class", "modal-dialog")
                .attr("role", "document")
                .append("div")
                .attr("class", "modal-content text-light bg-dark");
            let mhdr = mcnt.append("div")
                .attr("class", "modal-header");
            mhdr.append("h5")
                .attr("class", "modal-title")
                .attr("id", `image-build-title-${d.id}`)
                .text(`Image Build: ${d.id} ()`);
            mhdr.append("button")
                .attr("type", "button")
                .attr("class", "close text-light")
                .attr("data-dismiss", "modal")
                .attr("aria-label", "Close")
                .html('<span aria-hidden="true">&times;</span>');
            mcnt.append("div")
                .attr("class", "modal-body overflow-auto pod-logs")
                .append("div")
                .attr("class", "container-body pod-logs")
                .attr("id", `image-build-log-${d.id}`);
            let mftr = mcnt.append("div")
                .attr("class", "modal-footer");
            mftr.append("small")
                .attr("class", "text-muted pr-2")
                .attr("id", `image-build-elapsed-${d.id}`)
                .text("Elapsed: 3m 26s");
            mftr.append("button")
                .attr("type", "button")
                .attr("id", `image-modal-refresh-btn-${d.id}`)
                .attr("class", "btn btn-primary")
                .text("Refresh");
        }

        // update image build buttons if the build ID is available
        if (d.data.image !== null && d.data.image.build_id !== "") {
            let btn = document.getElementById(`image-build-btn-${d.id}`);
            btn.style.pointerEvents = "";
            btn.setAttribute("onclick", `refreshImageBuildLogs("${d.id}", "${d.data.image.build_id}");`);
            let refresh = document.getElementById(`image-modal-refresh-btn-${d.id}`);
            refresh.setAttribute("onclick", `refreshImageBuildLogs("${d.id}", "${d.data.image.build_id}");`);
        }

    });

    function getSVGClass(d) {
        switch (d.data.chart.status) {
            case "installing":
            case "upgrading":
                return "spinning-svg";
        }
        return "";
    }

    function getCircleVisibility(d) {
        if (d.data.image !== null) {
            if (d.data.image.error) {
                return "hidden";
            }
        }
        switch (d.data.chart.status) {
            case "waiting":
            case "installing":
            case "upgrading":
                return "visible";
        }
        return "hidden";
    }

    function getCircleClass(d) {
        switch (d.data.chart.status) {
            case "installing":
            case "upgrading":
                return "spinning-circle";
        }
        return "";
    }

    function getCircleStroke(d) {
        switch (d.data.chart.status) {
            case "waiting":
                return "gray";
            case "installing":
            case "upgrading":
                return "#2f3d4c";
        }
        return "#000000";
    }

    function getNodeIconVisibility(d) {
        if (d.data.image !== null) {
            if (d.data.image.error) {
                return "visible";
            }
        }
        switch (d.data.chart.status) {
            case "waiting":
            case "installing":
            case "upgrading":
                return "hidden";
        }
        return "visible";
    }

    function getNodeIconValue(d) {
        // https://fontawesome.com/cheatsheet/free/solid
        if (d.data.image !== null) {
            if (d.data.image.error) {
                return "\uf071";
            }
        }
        switch (d.data.chart.status) {
            case "done":
                return "\uf058";
            case "failed":
                return "\uf071";
        }
        return "\uf059";
    }

    function getNodeIconColor(d) {
        if (d.data.image !== null) {
            if (d.data.image.error) {
                return "#dc3545";
            }
        }
        switch (d.data.chart.status) {
            case "done":
                return "#28a745";
            case "failed":
                return "#dc3545";
        }
        return "#343a40";
    }

    // Declare the nodes
    let node = svg.selectAll("g.node")
        .data(nodes, function(d) { return d.id; });

    // Define the update selections

    node.select("circle")
        .attr("r", function(d) {
            return 10;
        })
        .attr("class", getCircleClass)
        .style("visibility", getCircleVisibility)
        .style("stroke", getCircleStroke)
        .style("fill", function(d) {
            switch (d.data.chart.status) {
                case "installing":
                case "upgrading":
                    return "#ffffff";
                case "waiting":
                case "done":
                case "failed":
                    break;
                default:
                    console.log(`circle: got unknown chart status: ${d.data.chart.status}`);
            }
            return "gray";
        });

    node.select("g")
        .attr("class", getSVGClass);

    node.select("rect")
        .style("visibility", getNodeIconVisibility);

    node.select(".fas")
        .style("visibility", getNodeIconVisibility)
        .style("fill", getNodeIconColor)
        .text(getNodeIconValue);

    let tt = d3.selectAll(".tree-tooltip").data(nodes);

    tt.select("h6")
        .html(function(d) { return d.id; });

    tt.select(".tt-repo-btn")
        .style("display", function(d) {
            return (d.data.image === null) ? "none" : "block";
        })
        .html(function(d) {
            const txt = d.depth === 0 ? "Triggering Repo" : "Repo Dependency";
            return `<img src="https://github.com/favicon.ico" height="16" width="16"> ${txt}`;
        });

    tt.select(".tt-helm-btn")
        .style("display", function(d) {
            return (d.data.image === null) ? "block" : "none";
        });

    tt.select(".tt-image-btn")
        .style("display", function(d) {
            return (d.data.image === null) ? "none" : "block";
        })
        .attr("class", function(d) {
            if (d.data.image === null) {
                return "tt-image-btn btn btn-primary btn-sm";
            }
            if (d.data.image.started === null) {
                return "tt-image-btn btn btn-secondary btn-sm";
            }
            if (d.data.image.completed === null) {
                return "tt-image-btn btn btn-primary btn-sm";
            }
            return (d.data.image.error) ? "tt-image-btn btn btn-danger btn-sm" : "tt-image-btn btn btn-success btn-sm";
        })
        .html(function(d) {
            if (d.data.image === null) {
                return "";
            }
            if (d.data.image.started === null) {
                return `Image: (not started)`;
            }
            let verb = "Building";
            let icon = "fa-spinner fa-pulse";
            if (d.data.image.completed != null) {
                verb = (d.data.image.error) ? "Error" : "Done";
                icon = (d.data.image.error) ? "fa-exclamation-circle" : "fa-check-circle";
            }
            const info = `<i class="fas ${icon}"></i>`;
            return `Image: ${verb} ${info}`;
        });

    tt.select(".tt-chart-btn")
        .style("display", function(d) {
            return (d.data.chart === null) ? "none" : "block";
        })
        .attr("class", function(d) {
            if (d.data.chart === null) {
                return "tt-chart-btn btn btn-secondary btn-sm";
            }
            switch (d.data.chart.status) {
            case "waiting":
                return "tt-chart-btn btn btn-secondary btn-sm";
            case "installing":
            case "upgrading":
                return "tt-chart-btn btn btn-warning btn-sm";
            case "done":
                return "tt-chart-btn btn btn-primary btn-sm";
            case "failed":
                return "tt-chart-btn btn btn-danger btn-sm";
            default:
                return "tt-chart-btn btn btn-secondary btn-sm";
            }
        })
        .text(function(d) {
            if (d.data.chart === null) {
                return "";
            }
            let start = d.data.chart.started !== null ? new Date(d.data.chart.started).getTime() : new Date().getTime();
            let end = new Date().getTime();
            switch (d.data.chart.status) {
                case "waiting":
                    if (d.data.image !== null && !d.data.image.error && d.data.image.completed === null) {
                        return "Chart: Waiting (image)";
                    } else {
                        if (d.children || d._children) {
                            return "Chart: Waiting (dependencies)";
                        }
                        return "Chart: Waiting";
                    }
                case "installing":
                    return `Chart: Installing (${millisToMinutesAndSeconds(end - start)})`;
                case "upgrading":
                    return `Chart: Upgrading (${millisToMinutesAndSeconds(end - start)})`;
                case "done":
                    end = new Date(d.data.chart.completed).getTime();
                    return `Chart: Done (${millisToMinutesAndSeconds(end - start)})`;
                case "failed":
                    end = new Date(d.data.chart.completed).getTime();
                    return `Chart: Failed (${millisToMinutesAndSeconds(end - start)})`;
                default:
                    return `Chart: Unknown (${d.data.chart.status})`;
            }
        });

    // Nodes should never be removed, but if they go missing from the model remove them
    node.exit().remove();

    // Define node entry
    let nodeEnter = node.enter().append("g")
        .on("mouseover", function(d) {
            if (currentVisibleTooltip !== null) {
                closeNodeTooltip(currentVisibleTooltip);
            }
            currentVisibleTooltip = `tooltip-${d.id}`;
            d3.select(`#tooltip-${d.id}`)
                .style("display", "block")
                .style("left", (d3.event.pageX ) + "px")
                .style("top", (d3.event.pageY) + "px")
                .transition()
                .duration(300)
                .style("opacity", 1);
        })
        .attr("class", "node")
        .attr("id", function(d) { return d.id; })
        .attr("transform", function(d) {
            return "translate(" + d.x + "," + d.y + ")"; });

    nodeEnter.append("g")
        .attr("class", getSVGClass)
        .append("circle")
        .attr("r", function(d) {
            return 10;
        })
        .attr("class", getCircleClass)
        .style("visibility", getCircleVisibility)
        .style("stroke", getCircleStroke);

    // this is an opaque box behind the node icon to prevent the path from being visible in the transparent parts of the icon
    nodeEnter.append("rect")
        .attr("x", function(d) { return this.parentNode.getBBox().x;})
        .attr("y", function(d, i) { return  this.parentNode.getBBox().y })
        .attr("width", function(d) { return this.parentNode.getBBox().width;})
        .attr("height", function(d) {return 20;})
        .style("fill", "#ffffff")
        .style("visibility", getNodeIconVisibility);

    nodeEnter.append("text")
        .attr("class", "fas")
        .attr("x", 0)
        .attr("y", 10)
        .attr("text-anchor", "middle")
        .attr("font-family", "Font Awesome 5 Free")
        .attr("font-weight", 900)
        .attr("font-size", 24)
        .style("opacity", 1)
        .style("visibility", getNodeIconVisibility)
        .style("fill", getNodeIconColor)
        .text(getNodeIconValue);

    nodeEnter.append("text")
        .attr("class", "node-label")
        .attr("y", function(d) {
            return d.children || d._children ? -26 : 20; })
        .attr("dy", ".35em")
        .attr("text-anchor", "middle")
        .attr("font-weight", 700)
        .text(function(d) {
            // root node label should not be elided
            if (d.depth === 0) {
                return d.id;
            }
            let name = d.id;
            if (name.length <= 10) {
                return name;
            }
            return name.substring(0, 9) + "...";
        })
        .style("fill-opacity", 1);

    // Declare the links
    let link = svg.selectAll("path.link")
        .data(links, function(d) { return d.target.id; });

    // Enter the links.
    link.enter().insert("path", "g")
        .attr("class", "link")
        .attr("d", diagonal);
}

// only allow one tooltip to be visible at a time
let currentVisibleTooltip;

function closeNodeTooltip(nodeid) {
    currentVisibleTooltip = null;
    d3.select(`#${nodeid}`)
        .transition()
        .duration(300)
        .style("opacity", 1e-6)
        .style("display", "none");
}

function refreshImageBuildLogs(nodeID, buildID) {
    let req = new XMLHttpRequest();

    req.open('GET', `${apiBaseURL}/v2/userenvs/${env_name}/imagebuild/${buildID}/events`, true);
    req.onload = function (e) {
        if (req.status !== 200) {
            console.log(`image build events request failed: ${req.status}: ${req.responseText}`);
            return;
        }

        let data = JSON.parse(req.response);
        if (data !== null) {
            let title = document.getElementById(`image-build-title-${nodeID}`);
            title.innerText = `Image Build: ${nodeID} (${data.status})`;
            let elapsed = document.getElementById(`image-build-elapsed-${nodeID}`);
            elapsed.innerText = data.elapsed;
            let logs = document.getElementById(`image-build-log-${nodeID}`);
            logs.innerText = data.events.join("\n");
        }
    };
    req.onerror = function (e) {
        console.error(`error getting image build events for ${env_name}/${buildID}: ${req.statusText}`);
    };

    req.send();
}


function updateLogs(logs) {
    document.getElementById("logsContainer").innerHTML = logs.join("<br \>\n");
    let objDiv = document.getElementById("logsContainer");
    objDiv.scrollTop = objDiv.scrollHeight;
}

// containerSelected calls get pod logs with the selected container
function containerSelected() {
    let containers = document.getElementById("selectContainerMenu");
    let container = containers.options[containers.selectedIndex].value;
    getPodLogs(container);
}

// setContainerSelectMenu replaces the existing menu
function setContainerSelectMenu(selectMenu) {
    if (selectMenu === null) {
        return;
    }
    let oldMenu = document.getElementById("selectContainerMenu");
    oldMenu.parentNode.replaceChild(selectMenu, oldMenu);
}

// renderContainerMenuOptions builds the container select menu from the containers array
function renderContainerMenuOptions(containers) {
    let selectMenu = document.createElement("select");
    selectMenu.className = "form-control";
    selectMenu.id = "selectContainerMenu";
    selectMenu.onchange = function(){containerSelected()};
    if (containers.length > 0) {
        for (let i = 0; i < containers.length; i++) {
            if (containers[i] !== "") {
                let opt = document.createElement("option");
                opt.id = `opt-container-${containers[i]}`;
                opt.value = containers[i];
                opt.innerHTML = containers[i];
                if (active_container === "") {
                    // active container defaults to the first valid container listed
                    active_container = containers[i];
                    getPodLogs(containers[i]);
                }
                selectMenu.appendChild(opt);
            }
        }
    }
    setContainerSelectMenu(selectMenu);
}

// getPodContainers gets a list of containers for specified pod
function getPodContainers() {
    let req = new XMLHttpRequest();

    req.open('GET', `${apiBaseURL}/v2/userenvs/${env_name}/namespace/pod/${active_pod_name}/containers`, true);
    req.onload = function (e) {
        if (req.status !== 200) {
            console.log(`pod containers request failed: ${req.status}: ${req.responseText}`);
            return;
        }

        let data = JSON.parse(req.response);
        if (data !== null) {
            renderContainerMenuOptions(data['containers']);
        }
    };
    req.onerror = function (e) {
        console.error(`error getting pod containers endpoint for ${env_name}: ${req.statusText}`);
    };

    req.send();
}

// setPodLogData replaces the existing logs
function setPodLogs(logs) {
    if (logs === null) {
        return;
    }
    let oldLogs = document.getElementById("podLogsBody");
    oldLogs.parentNode.replaceChild(logs, oldLogs);
}

// renderPodLogData replaces the logs
function renderPodLogs(data) {
    let logs = document.createElement('div');
    logs.innerText = data;
    logs.className = "container-body";
    logs.id = "podLogsBody";
    setPodLogs(logs);
}

// getPodLogs gets and renders the logs for the specified parameters
function getPodLogs(container) {
    let req = new XMLHttpRequest();

    if (container !== undefined) {
        active_container = container;
    }
    req.open('GET', `${apiBaseURL}/v2/userenvs/${env_name}/namespace/pod/${active_pod_name}/logs?container=${active_container}&lines=${pod_log_lines}`, true);
    req.onload = function (e) {
        if (req.status !== 200) {
            console.log(`pods logs request failed: ${req.status}: ${req.responseText}`);
            return;
        }

        let data = req.response;
        if (data !== null) {
            renderPodLogs(data);
        }
    };
    req.onerror = function (e) {
        console.error(`error getting pod logs endpoint for ${env_name}: ${req.statusText}`);
    };

    req.send();
}

// podLogModalData manages the pod logs modal and fetches the containers and logs
function podLogModalData(pod_name) {
    active_pod_name = pod_name;
    document.getElementById('podLogModalHeading').innerHTML = `Logs: ${active_pod_name}`;
    getPodContainers();
}

// setPodList replaces the table body with tbody
function setPodList(tbody) {
    let oldtbody = document.getElementById("k8sNamespacePodTableBody");
    if (tbody === null) {
        return;
    }
    oldtbody.parentNode.replaceChild(tbody, oldtbody);
    tbody.id = "k8sNamespacePodTableBody";
}

// podRow creates a table row from a pod object
function podRow(podValues) {
    let trPod = document.createElement("tr");
    trPod.id = `tr-pod`;
    for (let i = 0; i < podValues.length; i++) {
        let td = document.createElement("td");
        td.id = `td-pod-${podValues[0]}`;
        td.className = "text-left";
        if (i === 0) {
            let a = document.createElement("a");
            a.style.cursor = "pointer";
            a.id = `a-pod-${podValues[0]}`;
            a.innerHTML = podValues[0];
            a.setAttribute("data-toggle", "modal");
            a.setAttribute("data-target", "#podLogModal");
            a.addEventListener('click', function () {
                podLogModalData(a.innerHTML);
            });
            td.appendChild(a);
        } else {
            td.innerHTML = podValues[i];
        }
        trPod.appendChild(td);
    }
    return trPod;
}

// renderPodList renders the pod table rows with the pod data
function renderPodList(pods) {
    let tbody = document.createElement("tbody");
    const podHeadings = Object.keys(pods[0]);
    let trHeading = document.createElement("tr");
    trHeading.id = "tr-pod-headings";
    for (let i = 0; i < podHeadings.length; i++) {
        let th = document.createElement("th");
        th.id = `th-pod-${podHeadings[i]}`;
        th.className = "text-left";
        th.innerHTML = podHeadings[i].charAt(0).toUpperCase() + podHeadings[i].slice(1);
        trHeading.appendChild(th);
    }
    tbody.appendChild(trHeading);
    for (let i = 0; i < pods.length; i++) {
        let podValues = Object.values(pods[i]);
        let row = podRow(podValues);
        tbody.appendChild(row);
    }
    setPodList(tbody);
}

// getNamespacePods gets and renders the namespace pod list
function getNamespacePods(env_name) {
    let req = new XMLHttpRequest();

    req.open('GET', `${apiBaseURL}/v2/userenvs/${env_name}/namespace/pods`, true);
    req.onload = function (e) {
        if (req.status !== 200) {
            console.log(`namespace pods request failed: ${req.status}: ${req.responseText}`);
            return;
        }

        let podListData = JSON.parse(req.response);
        if (podListData !== null) {
            renderPodList(podListData);
        }
    };
    req.onerror = function (e) {
        console.error(`error getting namespace pod endpoint for ${env_name}: ${req.statusText}`);
    };

    req.send();
}

function sleep(ms) {
    return new Promise(resolve => setTimeout(resolve, ms));
}

// https://stackoverflow.com/questions/4959975/generate-random-number-between-two-numbers-in-javascript
function randomIntFromInterval(min, max) { // min and max included
    return Math.floor(Math.random() * (max - min + 1) + min);
}

async function update() {
    let req = new XMLHttpRequest(), req2 = new XMLHttpRequest();

    req.open('GET', statusEndpoint, true);
    req.onload = function(e) {
        if (req.status !== 200) {
            failures++;
            console.log(`event status request failed (${failures}): ${req.status}: ${req.responseText}`);
            if (failures >= 10) {
                console.log(`API failures exceed limit: ${failures}: aborting update`);
                clearInterval(updateInterval);
            }
            return;
        }

        const data = JSON.parse(req.response);

        if (data.hasOwnProperty('config')) {
            if (data.config.env_name !== null) {
                env_name = data.config.env_name;
                getNamespacePods(env_name);
            }
            updateConfig(data.config);
        } else {
            console.log("event status missing config element");
            return;
        }

        if (data.hasOwnProperty('tree')) {
            if (Object.keys(data.tree).length > 0) {
                updateTree(data.tree);
            }
        } else {
            console.log("event status missing tree element");
        }

        if (done) {
            clearInterval(updateInterval);
        }
    };
    req.onerror = function(e) {
        console.error(`error getting status endpoint: ${req.statusText}`);
    };

    req2.open('GET', logsEndpoint, true);
    req2.setRequestHeader("Acyl-Log-Key", logKey);
    req2.onload = function(e) {
        if (req2.status !== 200) {
            failures++;
            console.log(`event logs request failed (${failures}): ${req2.status}: ${req2.responseText}`);
            if (failures >= 10) {
                console.log(`API failures exceed limit: ${failures}: aborting update`);
                clearInterval(updateInterval);
            }
            return;
        }

        const data2 = JSON.parse(req2.response);

        if (data2 !== null) {
            updateLogs(data2);
        }
    };
    req2.onerror = function(e) {
        console.error(`error getting event log endpoint: ${req2.statusText}`);
    };

    // add some jitter to make the display seem smoother
    await sleep(randomIntFromInterval(1, 500));

    req.send(null);
    req2.send(null);
}

document.addEventListener("DOMContentLoaded", function(){
    update();
    if (document.getElementById('podLogModal') !== null) {
        $("#podLogModal").on('hidden.bs.modal', function (e) {
            active_pod_name = "";
            active_container = "";
            document.getElementById('podLogModalHeading').innerHTML = "Logs:";
            renderContainerMenuOptions([]);
            renderPodLogs("");
        });
        $("#podLogModalRefresh").on('click', function (e) {
            e.preventDefault();
            getPodLogs();
        });
    }
});
