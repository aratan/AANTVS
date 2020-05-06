const btnSwitch = document.querySelector('#switch');
btnSwitch.addEventListener('click',() => {
  document.body.classList.toggle('dark');
  btnSwitch.classList.toggle('active');
  // guarda datos localstore
  if (document.body.classList.contains('dark')){
      localStorage.setItem('noche','true');
  }else{
    localStorage.setItem('noche','false');
  }
});
// leer el modo actual
if(localStorage.getItem('noche') === 'true'){
  document.body.classList.add('dark');
}else{
  document.body.classList.remove('dark');
}



function carga(){ 
 
  setTimeout(()=>{
    //window.open("https://studio.emesal.org", target="_blank", "width=1100,height=800");
      document.getElementById("logo").innerHTML =  "{{ .CompanyName }}";
      document.getElementById("carga").innerHTML =  "Aquí puede ir tu publicidad.";
      mReloj()
    },5000);
  }

function mReloj(){
  var t = new Date()
  var hora = t.getHours()
  var minuto = t.getMinutes()
  var segundo = t.getSeconds()  
  var horaImprimible = hora + ":" + minuto + ":" + segundo
  //var horaImprimible = minuto + ":" + segundo
  document.getElementById("reloj").innerHTML = horaImprimible ;
// programacion 30 minutos
  if (hora + ":31" + ":1" == horaImprimible || hora + ":1" + ":1" == horaImprimible){
    window.location.reload("True")
  }
  setTimeout("mReloj()",1000)
}



