package main

import (
	"common"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type orchestrator struct {
	repoip      chan string
	deploystate chan map[string]common.Docker
	addip       chan string
	D			*common.Docker
}

func (o *orchestrator) StartState() {
	d := make(map[string]common.Docker)
	o.deploystate = make(chan map[string]common.Docker)
	o.addip = make(chan string)
	updatechan := make(chan common.Docker)
	for {
		select {
		case o.deploystate <- d:

		case ip := <-o.addip:
			_, exist := d[ip]
			if !exist {
				d[ip] = common.Docker{}
			}
			go o.pollDocker(ip, updatechan)

		case up := <-updatechan:
			//d[up.Ip] = up
		}
	}
}

func (o *orchestrator) WaitRefresh(t time.Time) {
	for {
		s := <-o.deploystate
		good := false
		for _, v := range s {
			if v.Updated.After(t) {
				good = true
				break

			}
		}
		if good {
			break
		}
		time.Sleep(10 * time.Second)
	}
}

func (o *orchestrator) StartRepository() {
	log.Print("index setup")
	registry_name := "samalba/docker-registry"
	// So that id is passed out of the function
	id := ""
	Img := &common.Image{}
	var err error
	var running bool
	for ; ; time.Sleep(10 * time.Second) {
		running, id, err = Img.IsRunning(o.D, registry_name)
		if err != nil {
			log.Print(err)
			continue
		}
		if !running {
			log.Print("index not running")
			err := Img.Load(o.D, registry_name)
			if err != nil {
				log.Print(err)
				continue
			}
			id, err = Img.Run(o.D, registry_name, false)
			if err != nil {
				log.Print(err)
				continue
			}
		}
		break
	}
	log.Print("index running id: ", id)
	
	C := &common.Container{}
	C.Id = id
	C.D = o.D
	err = C.Inspect()
	log.Print("fetched config")
	port := C.NetworkSettings.PortMapping.Tcp["5000"]

    host := o.D.GetIP() + ":" + port

	if err != nil {
		log.Print(err)
	}

	for {
		o.repoip <- host
	}

}

func (o *orchestrator) handleImage(w http.ResponseWriter, r *http.Request) {
	io.WriteString(w, "Waiting for index to be downloaded, this may take a while")
	repoip := <-o.repoip
	io.WriteString(w, "Recieved\n")
	
	Img := &common.Image{}
	
	tag := r.URL.Query()["name"]
	if len(tag) > 0 {
		io.WriteString(w, "Building image\n")
		err := Img.Build(o.D, r.Body, tag[0])
		if err != nil {
			io.WriteString(w, err.Error()+"\n")
			return
		}

		io.WriteString(w, "Tagging\n")
		repo_tag := repoip + "/" + tag[0]
		err = Img.AddTag(o.D, tag[0], repo_tag)
		if err != nil {
			io.WriteString(w, err.Error())
			return
		}
		io.WriteString(w, "Pushing to index\n")
		err = Img.Push(o.D, w, repo_tag)
		if err != nil {
			io.WriteString(w, err.Error())
			return
		}
	}
	io.WriteString(w, "built\n")
}

func (o *orchestrator) calcUpdate(w io.Writer, desired common.SkeletonDeployment, current map[string]common.Docker) (update map[string][]string) {
	c := fmt.Sprint(current)
	io.WriteString(w, c)
	io.WriteString(w, "\n")
	// Maps IP's to lists of containers to deploy
	update = make(map[string][]string)
	// For each container we want to deploy
	for container, _ := range desired.Containers {
		// Assuming granularity machine

		// For each machine check for container
		for ip, mInfo := range current {

			//Have we found the container
			found := false

			//Check if the container is running
			for _, checkContainer := range mInfo.Containers {

				imageName := checkContainer.Image

				//Get the actual name
				if strings.Contains(imageName, "/") {
					imageName = strings.SplitN(imageName, "/", 2)[1]
				}
				if strings.Contains(imageName, ":") {
					imageName = strings.SplitN(imageName, ":", 2)[0]
				}

				if imageName == container {
					found = true
					break
				}
			}

			//Do we need to deploy a image?
			if !found {
				update[ip] = append(update[ip], container)
			}

		}
	}

	return update

}

func (o *orchestrator) deploy(w http.ResponseWriter, r *http.Request) {

	io.WriteString(w, "Starting deploy\n")
	d := &common.SkeletonDeployment{}
	c, err := ioutil.ReadAll(r.Body)

	if err != nil {
		io.WriteString(w, err.Error())
		return
	}
	err = json.Unmarshal(c, d)
	if err != nil {
		io.WriteString(w, err.Error())
		return
	}

	for _, ip := range d.Machines.Ip {
		io.WriteString(w, "Adding ip\n")
		io.WriteString(w, ip)
		io.WriteString(w, "\n")
		o.addip <- ip
	}

	io.WriteString(w, "Waiting for image refreshes\n")
	o.WaitRefresh(time.Now())
	io.WriteString(w, "waited\n")

	current := <-o.deploystate

	diff := o.calcUpdate(w, *d, current)

	sdiff := fmt.Sprint(diff)
	io.WriteString(w, sdiff)
	io.WriteString(w, "\n")

	indexip := <-o.repoip

	io.WriteString(w, "Deploying diff\n")
	for ip, images := range diff {
		for _, container := range images {
            D := common.NewDocker(ip)
			Img := &common.Image{}
			
			io.WriteString(w, "Deploying "+container+" on "+ip+"\n")
			err := Img.Load(D, indexip+"/"+container)
			if err != nil {
				io.WriteString(w, err.Error())
				continue
			}
			id, err := Img.Run(D, indexip+"/"+container, false)
			io.WriteString(w, "Deployed \n")
			io.WriteString(w, id)
			io.WriteString(w, "\n")
			if err != nil {
				io.WriteString(w, err.Error())
			}
			io.WriteString(w, "\n")
		}
	}
}

func NewOrchestrator() (o *orchestrator) {
	o = new(orchestrator)
	o.repoip = make(chan string)
	go o.StartState()
	go o.StartRepository()
	o.D = common.NewDocker(os.Getenv("HOST"))
	return o
}

func main() {

	o := NewOrchestrator()

	http.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "orchestrator v0")
	})

	http.HandleFunc("/image", o.handleImage)

	http.HandleFunc("/deploy", o.deploy)

	log.Fatal(http.ListenAndServe(":900", nil))
}
